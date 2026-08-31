package ownership_test

import (
	"errors"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/apsis-io/velocity/ownership"
)

func mustFrozen[T any](t *testing.T, value T, opts ...ownership.Option[T]) *ownership.Frozen[T] {
	t.Helper()
	owner := mustOwner(t, value, opts...)
	frozen, err := owner.Freeze()
	if err != nil {
		t.Fatal(err)
	}
	return frozen
}

func TestFreezeConsumesOwnerAndReportsState(t *testing.T) {
	owner := mustOwner(t, 5)
	frozen, err := owner.Freeze()
	if err != nil {
		t.Fatal(err)
	}
	defer frozen.Release()

	if state := owner.State(); !state.Moved {
		t.Fatalf("owner after freeze = %+v", state)
	}
	if state := frozen.State(); !state.Frozen || state.Shared || state.Shares != 1 {
		t.Fatalf("frozen state = %+v", state)
	}
	if _, err := owner.Borrow(); !errors.Is(err, ownership.ErrMoved) {
		t.Fatalf("Borrow on frozen-away owner = %v", err)
	}
}

func TestFreezeRequiresExclusivity(t *testing.T) {
	owner := mustOwner(t, 1)
	defer owner.Release()
	borrow, err := owner.Borrow()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Freeze(); !errors.Is(err, ownership.ErrConflict) {
		t.Fatalf("Freeze with outstanding borrow = %v", err)
	}
	_ = borrow.Release()
	frozen, err := owner.Freeze()
	if err != nil {
		t.Fatalf("Freeze after release = %v", err)
	}
	_ = frozen.Release()
}

func TestFrozenReadsConcurrentlyAndCounts(t *testing.T) {
	frozen := mustFrozen(t, 9)

	clone, err := frozen.Clone()
	if err != nil {
		t.Fatal(err)
	}
	if state := frozen.State(); state.Shares != 2 {
		t.Fatalf("state = %+v", state)
	}

	first, err := frozen.Borrow()
	if err != nil {
		t.Fatal(err)
	}
	second, err := clone.Borrow()
	if err != nil {
		t.Fatalf("second concurrent read = %v", err)
	}
	if state := frozen.State(); state.Readers != 2 || state.Writer {
		t.Fatalf("state = %+v", state)
	}
	_ = first.Release()
	_ = second.Release()

	value, err := frozen.Read(func(access ownership.ReadAccess[int]) (int, error) {
		return access.Project(func(value int) (int, error) { return value, nil })
	})
	if err != nil || value != 9 {
		t.Fatalf("Read = (%d, %v)", value, err)
	}

	_ = clone.Release()
	_ = frozen.Release()
}

func TestFrozenIntoOwnerRequiresSoleHandle(t *testing.T) {
	frozen := mustFrozen(t, 3)
	clone, err := frozen.Clone()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := frozen.IntoOwner(); !errors.Is(err, ownership.ErrConflict) {
		t.Fatalf("IntoOwner with clone outstanding = %v", err)
	}
	if err := clone.Release(); err != nil {
		t.Fatal(err)
	}

	owner, err := frozen.IntoOwner()
	if err != nil {
		t.Fatalf("IntoOwner when sole = %v", err)
	}
	// Thawing restores mutability.
	if _, err := owner.Write(func(access ownership.WriteAccess[int]) (struct{}, error) {
		return access.Update(func(value *int) (struct{}, error) {
			*value *= 3
			return struct{}{}, nil
		})
	}); err != nil {
		t.Fatal(err)
	}
	got, err := owner.IntoValue()
	if err != nil || got != 9 {
		t.Fatalf("IntoValue = (%d, %v)", got, err)
	}
}

func TestFrozenDropRunsOnceOnFinalRelease(t *testing.T) {
	var drops atomic.Int32
	frozen := mustFrozen(t, []int{1, 2},
		ownership.WithDrop(func([]int) error { drops.Add(1); return nil }),
		ownership.WithClone(func(value []int) ([]int, error) { return slices.Clone(value), nil }),
	)
	clone, err := frozen.Clone()
	if err != nil {
		t.Fatal(err)
	}

	snapshot, err := frozen.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	snapshot[0] = 99
	original, err := frozen.Read(func(access ownership.ReadAccess[[]int]) (int, error) {
		return access.Project(func(value []int) (int, error) { return value[0], nil })
	})
	if err != nil || original != 1 {
		t.Fatalf("snapshot leaked into original: (%d, %v)", original, err)
	}

	if err := clone.Release(); err != nil {
		t.Fatal(err)
	}
	if drops.Load() != 0 {
		t.Fatalf("drop ran before final release: %d", drops.Load())
	}
	if err := frozen.Release(); err != nil {
		t.Fatal(err)
	}
	if err := frozen.Close(); err != nil {
		t.Fatalf("Close after Release = %v", err)
	}
	if drops.Load() != 1 {
		t.Fatalf("drops = %d, want 1", drops.Load())
	}
}

func TestFrozenReleaseBlockedByOwnBorrow(t *testing.T) {
	frozen := mustFrozen(t, 1)
	borrow, err := frozen.Borrow()
	if err != nil {
		t.Fatal(err)
	}
	if err := frozen.Release(); !errors.Is(err, ownership.ErrConflict) {
		t.Fatalf("Release with own borrow = %v", err)
	}
	_ = borrow.Release()
	if err := frozen.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestFrozenNilHandle(t *testing.T) {
	var frozen *ownership.Frozen[int]
	if state := frozen.State(); !state.Released {
		t.Fatalf("nil state = %+v", state)
	}
	if err := frozen.Release(); err != nil {
		t.Fatalf("nil Release = %v", err)
	}
	if _, err := frozen.Borrow(); !errors.Is(err, ownership.ErrReleased) {
		t.Fatalf("nil Borrow = %v", err)
	}
	if _, err := frozen.Clone(); !errors.Is(err, ownership.ErrReleased) {
		t.Fatalf("nil Clone = %v", err)
	}
	if _, err := frozen.IntoOwner(); !errors.Is(err, ownership.ErrReleased) {
		t.Fatalf("nil IntoOwner = %v", err)
	}
}
