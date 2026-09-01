package ownership_test

import (
	"errors"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/apsis-io/velocity/ownership"
)

func mustOwner[T any](t *testing.T, value T, opts ...ownership.Option[T]) *ownership.Owner[T] {
	t.Helper()
	owner, err := ownership.New(value, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return owner
}

func TestOwnerViewAndMutate(t *testing.T) {
	owner := mustOwner(t, 10)
	got, err := owner.View(func(value int) (string, error) { return "value", nil })
	if err != nil || got != "value" {
		t.Fatalf("View = (%q, %v)", got, err)
	}
	// A returned error does not roll the mutation back.
	gotInt, err := owner.Mutate(func(value *int) (int, error) {
		*value += 5
		return *value, errors.New("committed")
	})
	if gotInt != 15 || err == nil || err.Error() != "committed" {
		t.Fatalf("Mutate = (%d, %v)", gotInt, err)
	}
	value, err := owner.View(func(value int) (int, error) { return value, nil })
	if err != nil || value != 15 {
		t.Fatalf("committed value = (%d, %v)", value, err)
	}
	// Nil callbacks are rejected before any borrow is taken.
	if _, err := owner.View[int](nil); !errors.Is(err, ownership.ErrProjection) {
		t.Fatalf("nil View = %v", err)
	}
	if err := owner.WithWrite(nil); !errors.Is(err, ownership.ErrProjection) {
		t.Fatalf("nil WithWrite = %v", err)
	}
	if state := owner.State(); state.Readers != 0 || state.Writer {
		t.Fatalf("state after rejected callbacks = %+v", state)
	}
}

func TestBorrowConflictsAndRelease(t *testing.T) {
	owner := mustOwner(t, 1)
	first, err := owner.Borrow()
	if err != nil {
		t.Fatal(err)
	}
	second, err := owner.Borrow()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.BorrowMut(); !errors.Is(err, ownership.ErrConflict) {
		t.Fatalf("BorrowMut error = %v", err)
	}
	var conflict *ownership.ConflictError
	if _, err := owner.Move(); !errors.As(err, &conflict) || conflict.Readers != 2 {
		t.Fatalf("Move error = %#v", err)
	}
	if err := first.Release(); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if err := second.Release(); err != nil {
		t.Fatal(err)
	}

	writer, err := owner.BorrowMut()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Borrow(); !errors.Is(err, ownership.ErrConflict) {
		t.Fatalf("Borrow error = %v", err)
	}
	if _, err := writer.Update(func(value *int) (int, error) { *value++; return *value, nil }); err != nil {
		t.Fatal(err)
	}
	if err := writer.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestMoveIntoValueAndCleanup(t *testing.T) {
	var drops atomic.Int32
	owner := mustOwner(t, 7, ownership.WithDrop(func(int) error { drops.Add(1); return nil }))
	moved, err := owner.Move()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Borrow(); !errors.Is(err, ownership.ErrMoved) {
		t.Fatalf("old Borrow error = %v", err)
	}
	if err := owner.Release(); err != nil {
		t.Fatal(err)
	}
	value, err := moved.Detach()
	if err != nil || value != 7 {
		t.Fatalf("Detach = (%d, %v)", value, err)
	}
	if err := moved.Close(); err != nil {
		t.Fatal(err)
	}
	if drops.Load() != 0 {
		t.Fatalf("drops = %d", drops.Load())
	}
}

func TestSharedCloneReleaseAndIntoOwner(t *testing.T) {
	owner := mustOwner(t, 3)
	shared, err := owner.IntoShared()
	if err != nil {
		t.Fatal(err)
	}
	clone, err := shared.Clone()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := shared.IntoOwner(); !errors.Is(err, ownership.ErrConflict) {
		t.Fatalf("IntoOwner with clone = %v", err)
	}
	if err := clone.Release(); err != nil {
		t.Fatal(err)
	}
	unwrapped, err := shared.IntoOwner()
	if err != nil {
		t.Fatal(err)
	}
	if err := shared.Close(); err != nil {
		t.Fatal(err)
	}
	value, err := unwrapped.Detach()
	if err != nil || value != 3 {
		t.Fatalf("Detach = (%d, %v)", value, err)
	}
}

func TestSharedReleaseRules(t *testing.T) {
	var drops atomic.Int32
	owner := mustOwner(t, 1, ownership.WithDrop(func(int) error { drops.Add(1); return nil }))
	first, _ := owner.IntoShared()
	second, _ := first.Clone()
	borrow, _ := second.Borrow()
	if err := first.Release(); err != nil {
		t.Fatalf("release unrelated handle = %v", err)
	}
	if err := second.Release(); !errors.Is(err, ownership.ErrConflict) {
		t.Fatalf("release issuing handle = %v", err)
	}
	if err := borrow.Release(); err != nil {
		t.Fatal(err)
	}
	if err := second.Release(); err != nil {
		t.Fatal(err)
	}
	if drops.Load() != 1 {
		t.Fatalf("drops = %d", drops.Load())
	}
}

func TestDropCanReenterOwnerRelease(t *testing.T) {
	var owner *ownership.Owner[int]
	owner = mustOwner(t, 9, ownership.WithDrop(func(int) error {
		return owner.Release()
	}))
	done := make(chan error, 1)
	go func() { done <- owner.Release() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Release = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("reentrant Owner.Release deadlocked")
	}
}

func TestDropCanReenterFinalSharedRelease(t *testing.T) {
	var reentrant *ownership.Shared[int]
	owner := mustOwner(t, 9, ownership.WithDrop(func(int) error {
		return reentrant.Release()
	}))
	var err error
	reentrant, err = owner.IntoShared()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- reentrant.Release() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Release = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("reentrant Shared.Release deadlocked")
	}
}

func TestDropRunsTerminallyOnceAndRetainsError(t *testing.T) {
	dropErr := errors.New("drop")
	var calls atomic.Int32
	var owner *ownership.Owner[int]
	owner = mustOwner(t, 9, ownership.WithDrop(func(int) error {
		calls.Add(1)
		if state := owner.State(); !state.Released {
			t.Fatalf("state during drop = %+v", state)
		}
		if _, err := owner.Borrow(); !errors.Is(err, ownership.ErrReleased) {
			t.Fatalf("Borrow during drop = %v", err)
		}
		return dropErr
	}))
	if err := owner.Release(); !errors.Is(err, dropErr) {
		t.Fatalf("Release = %v", err)
	}
	if err := owner.Close(); err != nil {
		t.Fatalf("second Close = %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d", calls.Load())
	}
	if !errors.Is(owner.State().DropError, dropErr) {
		t.Fatalf("state DropError = %v", owner.State().DropError)
	}
}

func TestSnapshotChecksHandleBeforeCloneConfiguration(t *testing.T) {
	owner := mustOwner(t, 1)
	moved, err := owner.Move()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Snapshot(); !errors.Is(err, ownership.ErrMoved) {
		t.Fatalf("moved Snapshot = %v", err)
	}
	if _, err := moved.Snapshot(); !errors.Is(err, ownership.ErrNoClone) {
		t.Fatalf("live Snapshot = %v", err)
	}
	if err := moved.Release(); err != nil {
		t.Fatal(err)
	}
	if _, err := moved.Snapshot(); !errors.Is(err, ownership.ErrReleased) {
		t.Fatalf("released Snapshot = %v", err)
	}

	owner = mustOwner(t, 1)
	shared, err := owner.IntoShared()
	if err != nil {
		t.Fatal(err)
	}
	unwrapped, err := shared.IntoOwner()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := shared.Snapshot(); !errors.Is(err, ownership.ErrMoved) {
		t.Fatalf("unwrapped Shared Snapshot = %v", err)
	}
	if err := unwrapped.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotAndCloneValidation(t *testing.T) {
	owner := mustOwner(t, []int{1, 2})
	if _, err := owner.Snapshot(); !errors.Is(err, ownership.ErrNoClone) {
		t.Fatalf("Snapshot without clone = %v", err)
	}
	owner = mustOwner(t, []int{1, 2}, ownership.WithClone(func(value []int) ([]int, error) {
		return slices.Clone(value), nil
	}))
	copyValue, err := owner.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	copyValue[0] = 99
	original, err := owner.View(func(value []int) (int, error) { return value[0], nil })
	if err != nil || original != 1 {
		t.Fatalf("original = (%d, %v)", original, err)
	}
}

func TestOptionsAndProjectionErrors(t *testing.T) {
	_, err := ownership.New(1, (ownership.Option[int])(nil))
	if !errors.Is(err, ownership.ErrInvalidConfig) || !errors.Is(err, ownership.ErrNilOption) || errors.Is(err, ownership.ErrProjection) {
		t.Fatalf("nil option = %v", err)
	}
	var configErr *ownership.ConfigError
	if !errors.As(err, &configErr) || configErr.Option != "option 0" {
		t.Fatalf("nil option wrapper = %#v", err)
	}
	if _, err := ownership.New(1, ownership.WithDrop[int](nil)); !errors.Is(err, ownership.ErrInvalidConfig) {
		t.Fatalf("nil drop = %v", err)
	}
	if _, err := ownership.New(1, ownership.WithClone[int](nil)); !errors.Is(err, ownership.ErrInvalidConfig) {
		t.Fatalf("nil clone = %v", err)
	}
	if _, err := ownership.New(1, ownership.WithClone(func(value int) (int, error) { return value, nil }), ownership.WithClone(func(value int) (int, error) { return value, nil })); !errors.Is(err, ownership.ErrDuplicateOption) {
		t.Fatalf("duplicate clone = %v", err)
	}
	owner := mustOwner(t, 1)
	if _, err := owner.View[string](nil); !errors.Is(err, ownership.ErrProjection) {
		t.Fatalf("nil View callback = %v", err)
	}
	if _, err := owner.Mutate[string](nil); !errors.Is(err, ownership.ErrProjection) {
		t.Fatalf("nil Mutate callback = %v", err)
	}
}

func TestNilAndZeroHandles(t *testing.T) {
	var owner *ownership.Owner[int]
	if _, err := owner.Borrow(); !errors.Is(err, ownership.ErrReleased) {
		t.Fatalf("nil owner Borrow = %v", err)
	}
	if err := owner.Release(); err != nil {
		t.Fatalf("nil owner Release = %v", err)
	}
	var shared *ownership.Shared[int]
	if _, err := shared.Clone(); !errors.Is(err, ownership.ErrReleased) {
		t.Fatalf("nil shared Clone = %v", err)
	}
	var read ownership.ReadBorrow[int]
	if _, err := read.Project(func(value int) (int, error) { return value, nil }); !errors.Is(err, ownership.ErrReleased) {
		t.Fatalf("zero borrow Project = %v", err)
	}
}
