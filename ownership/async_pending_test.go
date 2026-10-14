package ownership_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/apsis-io/velocity/ownership"
)

// A queued mutation is not a borrow: o.h.borrows is not incremented until
// admission. So a submission the caller has already been handed a Future for is
// invisible to the operations that end a handle's life — Release runs the Drop
// and hands the value away while the mutation is still queued behind another
// borrow.
//
// Whether that is coherent is a question, not a bug. The state at the end is
// consistent: the Drop ran, the mutation did not apply, and the mutation
// reports ReleasedError. What is missing is that whoever called Release had no
// way to know a mutation was outstanding, so they could not drain first.
func TestReleaseRefusesWhileAMutationIsQueued(t *testing.T) {
	dropped := false

	owner, err := ownership.New(0, ownership.WithDrop(func(int) error {
		dropped = true
		return nil
	}))
	if err != nil {
		t.Fatal(err)
	}

	// Hold a borrow so the mutation queues rather than running.
	held, err := owner.BorrowMut()
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	entered := make(chan struct{})
	releaseFn := make(chan struct{})

	queued := owner.MutateAsync(ctx, func(v *int) (int, error) {
		close(entered)
		<-releaseFn

		*v = 99

		return *v, nil
	})
	if _, ready := queued.Try(); ready {
		t.Fatal("the mutation ran while a write borrow was held")
	}

	if err := held.Release(); err != nil {
		t.Fatal(err)
	}

	// Releasing the borrow unblocks the queued mutation's admission, and
	// admission plus completion of a trivial fn can beat this goroutine to
	// the next line on a loaded runner. The mutation signals entry instead:
	// once it is running, the outstanding count is at its peak and the
	// refusal below is deterministic rather than a won race.
	<-entered

	// The submission is outstanding, so ending the handle must be refused. It
	// is not a borrow — no address is exposed and nothing is excluded by it —
	// so it carries its own count; without it, whether the Drop ran before or
	// after the submitted mutation was a race the caller could not see.
	if state := owner.State(); state.Pending != 1 {
		t.Fatalf("State.Pending = %d, want 1", state.Pending)
	}

	if err := owner.Release(); !errors.Is(err, ownership.ErrConflict) {
		t.Fatalf("Release with a queued mutation = %v, want a conflict", err)
	}

	if dropped {
		t.Fatal("the Drop ran while a submitted mutation was still outstanding")
	}

	// Let it finish: the work the caller committed runs, and the count falls
	// with it.
	close(releaseFn)

	res, err := queued.Await(ctx)
	if err != nil || res != 99 {
		t.Fatalf("the mutation = (%+v, %v), want 99 and no error", res, err)
	}

	// And the cell is not left permanently blocked: with nothing outstanding,
	// Release succeeds and the Drop runs.
	if err := owner.Release(); err != nil {
		t.Fatalf("Release after the mutation completed = %v, want success", err)
	}

	if !dropped {
		t.Fatal("the Drop did not run once nothing was outstanding")
	}
}

// Every operation that consumes or transitions the Owner handle — Move, Detach,
// IntoShared, Freeze, Map, Release — must refuse while a submitted mutation is
// still pending, even before its goroutine has acquired the write borrow.
func TestTransitionsRefuseWhileAMutationIsPending(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	owner := ownership.Own(10)
	defer owner.Release()

	releaseFn := make(chan struct{})
	f := owner.WithWriteAsync(ctx, func(v *int) error {
		<-releaseFn

		*v = 20

		return nil
	})

	for _, tc := range []struct {
		name string
		call func() error
	}{
		{"Move", func() error { _, err := owner.Move(); return err }},
		{"Detach", func() error { _, err := owner.Detach(); return err }},
		{"IntoShared", func() error { _, err := owner.IntoShared(); return err }},
		{"Freeze", func() error { _, err := owner.Freeze(); return err }},
		{"Map", func() error { _, err := owner.Map(func(v int) (int, error) { return v, nil }); return err }},
		{"Release", owner.Release},
	} {
		err := tc.call()
		if !errors.Is(err, ownership.ErrConflict) {
			t.Fatalf("%s with a pending mutation = %v, want ErrConflict", tc.name, err)
		}

		var ce *ownership.ConflictError
		if !errors.As(err, &ce) || ce.Pending != 1 {
			t.Fatalf("%s ConflictError = %+v, want Pending=1", tc.name, ce)
		}
	}

	close(releaseFn)

	if _, err := f.Await(ctx); err != nil {
		t.Fatalf("WithWriteAsync Await = %v", err)
	}

	got, err := owner.View(func(v int) (int, error) { return v, nil })
	if err != nil || got != 20 {
		t.Fatalf("value after WithWriteAsync = (%d, %v), want 20", got, err)
	}
}

// Sealing remains the way to discard pending work deliberately, so the rule
// above is a choice rather than a dead end.
func TestSealDiscardsAQueuedMutationDeliberately(t *testing.T) {
	dropped := false

	owner, err := ownership.New(0, ownership.WithDrop(func(int) error {
		dropped = true
		return nil
	}))
	if err != nil {
		t.Fatal(err)
	}

	held, err := owner.BorrowMut()
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	queued := owner.MutateAsync(ctx, func(v *int) (int, error) {
		t.Error("a sealed mutation ran its callback")

		*v = 99

		return *v, nil
	})

	if err := owner.Seal(); err != nil {
		t.Fatalf("Seal = %v", err)
	}

	_ = held.Release()

	// The mutation is refused by the seal, and says so.
	_, err = queued.Await(ctx)
	if !errors.Is(err, ownership.ErrSealed) {
		t.Fatalf("the sealed mutation = %v, want ErrSealed", err)
	}

	// And once it has reported, Release goes through.
	if err := owner.Release(); err != nil {
		t.Fatalf("Release after a sealed mutation = %v", err)
	}

	if !dropped {
		t.Fatal("the Drop did not run")
	}
}

// The mirror: a queued mutation is not counted, so a caller who drains with
// WaitContext can be told "nothing outstanding" while one is still queued. This
// is the property that would make a queued mutation safe to retire against, and
// it does not hold.
func TestSealDoesNotSeeAQueuedMutation(t *testing.T) {
	owner, _ := ownership.New(0)

	held, _ := owner.BorrowMut()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	owner.MutateAsync(ctx, func(v *int) (int, error) { return *v, nil })

	// Seal refuses new borrows. A queued mutation is not a borrow, so this
	// succeeds — and the mutation is then refused by the seal, which is the
	// right outcome, but only because seal also changed the state it checks.
	if err := owner.Seal(); err != nil {
		t.Fatalf("Seal with a queued mutation = %v", err)
	}

	_ = held.Release()

	// The queued mutation is refused by the seal, which is the right outcome —
	// but it got there through the seal changing the state admission checks,
	// not through the seal having seen the mutation.
}
