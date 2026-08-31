package ownership_test

import (
	"errors"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/apsis-io/velocity/ownership"
)

func TestNewSharedBorrowsAndScopedAccess(t *testing.T) {
	shared, err := ownership.NewShared(1)
	if err != nil {
		t.Fatal(err)
	}
	read, err := shared.Borrow()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := shared.BorrowMut(); !errors.Is(err, ownership.ErrConflict) {
		t.Fatalf("BorrowMut with reader = %v", err)
	}
	_ = read.Release()
	write, err := shared.BorrowMut()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := shared.Borrow(); !errors.Is(err, ownership.ErrConflict) {
		t.Fatalf("Borrow with writer = %v", err)
	}
	_, err = write.Update(func(value *int) (struct{}, error) {
		*value++
		return struct{}{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = write.Release()
	_, err = shared.Write(func(access ownership.WriteAccess[int]) (struct{}, error) {
		return access.Update(func(value *int) (struct{}, error) {
			*value++
			return struct{}{}, nil
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	value, err := shared.Read(func(access ownership.ReadAccess[int]) (int, error) {
		return access.Project(func(value int) (int, error) { return value, nil })
	})
	if err != nil || value != 3 {
		t.Fatalf("Read = (%d, %v)", value, err)
	}
	_ = shared.Release()
}

func TestNewSharedCloneAndIntoOwner(t *testing.T) {
	shared, err := ownership.NewShared(1)
	if err != nil {
		t.Fatal(err)
	}
	clone, err := shared.Clone()
	if err != nil {
		t.Fatal(err)
	}
	if state := shared.State(); state.Shares != 2 {
		t.Fatalf("state = %+v", state)
	}
	if _, err := shared.IntoOwner(); !errors.Is(err, ownership.ErrConflict) {
		t.Fatalf("IntoOwner with clone = %v", err)
	}
	_ = clone.Release()
	owner, err := shared.IntoOwner()
	if err != nil {
		t.Fatal(err)
	}
	value, err := owner.IntoValue()
	if err != nil || value != 1 {
		t.Fatalf("IntoValue = (%d, %v)", value, err)
	}
}

func TestNewSharedDropAndSnapshot(t *testing.T) {
	var drops atomic.Int32
	shared, err := ownership.NewShared([]int{1, 2},
		ownership.WithDrop(func([]int) error { drops.Add(1); return nil }),
		ownership.WithClone(func(value []int) ([]int, error) { return slices.Clone(value), nil }),
	)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := shared.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	snapshot[0] = 9
	original, err := shared.Read(func(access ownership.ReadAccess[[]int]) (int, error) {
		return access.Project(func(value []int) (int, error) { return value[0], nil })
	})
	if err != nil || original != 1 {
		t.Fatalf("original = (%d, %v)", original, err)
	}
	if err := shared.Release(); err != nil {
		t.Fatal(err)
	}
	if err := shared.Close(); err != nil {
		t.Fatal(err)
	}
	if drops.Load() != 1 {
		t.Fatalf("drops = %d", drops.Load())
	}
}

func TestNewSharedOptionValidation(t *testing.T) {
	if _, err := ownership.NewShared(1, (ownership.Option[int])(nil)); !errors.Is(err, ownership.ErrNilOption) {
		t.Fatalf("nil option = %v", err)
	}
}
