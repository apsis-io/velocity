package ownership_test

import (
	"errors"
	"testing"

	"github.com/apsis-io/velocity/ownership"
)

// Untracked borrows differ from tracked ones only in leak reclamation, so the
// borrow state machine must behave identically through them.
func TestUntrackedBorrowEnforcesSameConflicts(t *testing.T) {
	t.Run("read excludes write", func(t *testing.T) {
		owner := mustOwner(t, 1)
		defer owner.Release()
		read, err := owner.BorrowUntracked()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := owner.BorrowMut(); !errors.Is(err, ownership.ErrConflict) {
			t.Fatalf("BorrowMut during untracked read = %v", err)
		}
		if _, err := owner.BorrowMutUntracked(); !errors.Is(err, ownership.ErrConflict) {
			t.Fatalf("BorrowMutUntracked during untracked read = %v", err)
		}
		if err := read.Release(); err != nil {
			t.Fatal(err)
		}
		write, err := owner.BorrowMut()
		if err != nil {
			t.Fatalf("BorrowMut after release = %v", err)
		}
		_ = write.Release()
	})

	t.Run("write excludes read", func(t *testing.T) {
		owner := mustOwner(t, 1)
		defer owner.Release()
		write, err := owner.BorrowMutUntracked()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := owner.Borrow(); !errors.Is(err, ownership.ErrConflict) {
			t.Fatalf("Borrow during untracked write = %v", err)
		}
		if _, err := owner.BorrowUntracked(); !errors.Is(err, ownership.ErrConflict) {
			t.Fatalf("BorrowUntracked during untracked write = %v", err)
		}
		if err := write.Release(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("tracked read coexists with untracked read", func(t *testing.T) {
		owner := mustOwner(t, 1)
		defer owner.Release()
		tracked, err := owner.Borrow()
		if err != nil {
			t.Fatal(err)
		}
		untracked, err := owner.BorrowUntracked()
		if err != nil {
			t.Fatalf("BorrowUntracked alongside tracked read = %v", err)
		}
		if state := owner.State(); state.Readers != 2 {
			t.Fatalf("state = %+v", state)
		}
		_ = tracked.Release()
		_ = untracked.Release()
	})
}

func TestUntrackedBorrowProjectsAndUpdates(t *testing.T) {
	owner := mustOwner(t, 4)
	defer owner.Release()

	write, err := owner.BorrowMutUntracked()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := write.Update(func(value *int) (struct{}, error) {
		*value *= 2
		return struct{}{}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := write.Release(); err != nil {
		t.Fatal(err)
	}

	read, err := owner.BorrowUntracked()
	if err != nil {
		t.Fatal(err)
	}
	value, err := read.Project(func(value int) (int, error) { return value, nil })
	if err != nil || value != 8 {
		t.Fatalf("Project = (%d, %v)", value, err)
	}
	if err := read.Release(); err != nil {
		t.Fatal(err)
	}
}

// Release runs Stop on a zero runtime.Cleanup for untracked borrows, which
// must stay a no-op across repeated calls.
func TestUntrackedBorrowReleaseIsIdempotent(t *testing.T) {
	owner := mustOwner(t, 1)
	defer owner.Release()

	read, err := owner.BorrowUntracked()
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := read.Release(); err != nil {
			t.Fatalf("Release = %v", err)
		}
	}
	if _, err := read.Project(func(value int) (int, error) { return value, nil }); !errors.Is(err, ownership.ErrReleased) {
		t.Fatalf("Project after release = %v", err)
	}

	write, err := owner.BorrowMutUntracked()
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := write.Close(); err != nil {
			t.Fatalf("Close = %v", err)
		}
	}
}

func TestSharedUntrackedBorrow(t *testing.T) {
	owner := mustOwner(t, 7)
	shared, err := owner.IntoShared()
	if err != nil {
		t.Fatal(err)
	}
	defer shared.Release()

	read, err := shared.BorrowUntracked()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := shared.BorrowMutUntracked(); !errors.Is(err, ownership.ErrConflict) {
		t.Fatalf("BorrowMutUntracked during read = %v", err)
	}
	value, err := read.Project(func(value int) (int, error) { return value, nil })
	if err != nil || value != 7 {
		t.Fatalf("Project = (%d, %v)", value, err)
	}
	_ = read.Release()
}
