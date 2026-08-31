package ownership_test

import (
	"errors"
	"testing"

	"github.com/apsis-io/velocity/ownership"
)

type fakeCloser struct {
	closed int
	err    error
}

func (f *fakeCloser) Close() error {
	f.closed++
	return f.err
}

func TestViewAndMutate(t *testing.T) {
	owner := mustOwner(t, 4)
	defer owner.Release()

	doubled, err := owner.Mutate(func(value *int) (int, error) {
		*value *= 2
		return *value, nil
	})
	if err != nil || doubled != 8 {
		t.Fatalf("Mutate = (%d, %v)", doubled, err)
	}
	got, err := owner.View(func(value int) (int, error) { return value + 1, nil })
	if err != nil || got != 9 {
		t.Fatalf("View = (%d, %v)", got, err)
	}
}

func TestViewAndMutatePropagateErrors(t *testing.T) {
	owner := mustOwner(t, 1)
	defer owner.Release()
	wantErr := errors.New("boom")

	if _, err := owner.View(func(int) (int, error) { return 0, wantErr }); !errors.Is(err, wantErr) {
		t.Fatalf("View = %v", err)
	}
	if _, err := owner.Mutate(func(*int) (int, error) { return 0, wantErr }); !errors.Is(err, wantErr) {
		t.Fatalf("Mutate = %v", err)
	}
	if err := owner.WithRead(func(int) error { return wantErr }); !errors.Is(err, wantErr) {
		t.Fatalf("WithRead = %v", err)
	}
	if err := owner.WithWrite(func(*int) error { return wantErr }); !errors.Is(err, wantErr) {
		t.Fatalf("WithWrite = %v", err)
	}
}

// The convenience wrappers must keep the borrow rules, not bypass them.
func TestViewAndMutateEnforceBorrowRules(t *testing.T) {
	owner := mustOwner(t, 1)
	defer owner.Release()

	_, err := owner.View(func(int) (int, error) {
		// A write is impossible while this read borrow is live.
		if _, err := owner.BorrowMut(); !errors.Is(err, ownership.ErrConflict) {
			return 0, errors.New("BorrowMut inside View should conflict, got " + errString(err))
		}
		return 1, nil
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = owner.Mutate(func(*int) (int, error) {
		if _, err := owner.Borrow(); !errors.Is(err, ownership.ErrConflict) {
			return 0, errors.New("Borrow inside Mutate should conflict, got " + errString(err))
		}
		return 1, nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func errString(err error) string {
	if err == nil {
		return "nil"
	}
	return err.Error()
}

func TestWithReadAndWithWrite(t *testing.T) {
	owner := mustOwner(t, 2)
	defer owner.Release()

	if err := owner.WithWrite(func(value *int) error { *value += 5; return nil }); err != nil {
		t.Fatal(err)
	}
	seen := 0
	if err := owner.WithRead(func(value int) error { seen = value; return nil }); err != nil {
		t.Fatal(err)
	}
	if seen != 7 {
		t.Fatalf("seen = %d, want 7", seen)
	}
}

func TestConvenienceNilCallbacks(t *testing.T) {
	owner := mustOwner(t, 1)
	defer owner.Release()

	if _, err := owner.View[int](nil); !errors.Is(err, ownership.ErrProjection) {
		t.Fatalf("View(nil) = %v", err)
	}
	if _, err := owner.Mutate[int](nil); !errors.Is(err, ownership.ErrProjection) {
		t.Fatalf("Mutate(nil) = %v", err)
	}
	if err := owner.WithRead(nil); !errors.Is(err, ownership.ErrProjection) {
		t.Fatalf("WithRead(nil) = %v", err)
	}
	if err := owner.WithWrite(nil); !errors.Is(err, ownership.ErrProjection) {
		t.Fatalf("WithWrite(nil) = %v", err)
	}
}

func TestSharedAndFrozenConvenience(t *testing.T) {
	owner := mustOwner(t, 3)
	shared, err := owner.IntoShared()
	if err != nil {
		t.Fatal(err)
	}
	if err := shared.WithWrite(func(value *int) error { *value++; return nil }); err != nil {
		t.Fatal(err)
	}
	got, err := shared.View(func(value int) (int, error) { return value, nil })
	if err != nil || got != 4 {
		t.Fatalf("Shared.View = (%d, %v)", got, err)
	}
	back, err := shared.IntoOwner()
	if err != nil {
		t.Fatal(err)
	}

	frozen, err := back.Freeze()
	if err != nil {
		t.Fatal(err)
	}
	defer frozen.Release()
	got, err = frozen.View(func(value int) (int, error) { return value * 2, nil })
	if err != nil || got != 8 {
		t.Fatalf("Frozen.View = (%d, %v)", got, err)
	}
	seen := 0
	if err := frozen.WithRead(func(value int) error { seen = value; return nil }); err != nil {
		t.Fatal(err)
	}
	if seen != 4 {
		t.Fatalf("Frozen.WithRead saw %d", seen)
	}
}

func TestNewCloserClosesExactlyOnce(t *testing.T) {
	closer := &fakeCloser{}
	owner := ownership.NewCloser[*fakeCloser](closer)

	if err := owner.Release(); err != nil {
		t.Fatal(err)
	}
	if err := owner.Close(); err != nil {
		t.Fatalf("second Close = %v", err)
	}
	if closer.closed != 1 {
		t.Fatalf("closed %d times, want 1", closer.closed)
	}
}

func TestNewCloserReportsCloseError(t *testing.T) {
	wantErr := errors.New("close failed")
	owner := ownership.NewCloser[*fakeCloser](&fakeCloser{err: wantErr})
	if err := owner.Release(); !errors.Is(err, wantErr) {
		t.Fatalf("Release = %v", err)
	}
	if state := owner.State(); !errors.Is(state.DropError, wantErr) {
		t.Fatalf("State.DropError = %v", state.DropError)
	}
}

func TestNewFrozenPublishesReadOnly(t *testing.T) {
	frozen, err := ownership.NewFrozen(42)
	if err != nil {
		t.Fatal(err)
	}
	defer frozen.Release()

	if state := frozen.State(); !state.Frozen || state.Shares != 1 {
		t.Fatalf("state = %+v", state)
	}
	got, err := frozen.View(func(value int) (int, error) { return value, nil })
	if err != nil || got != 42 {
		t.Fatalf("View = (%d, %v)", got, err)
	}

	// Thawing is still available for the sole handle.
	owner, err := frozen.IntoOwner()
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.WithWrite(func(value *int) error { *value++; return nil }); err != nil {
		t.Fatal(err)
	}
	value, err := owner.Detach()
	if err != nil || value != 43 {
		t.Fatalf("Detach = (%d, %v)", value, err)
	}
}

func TestNewFrozenRejectsBadOption(t *testing.T) {
	if _, err := ownership.NewFrozen(1, (ownership.Option[int])(nil)); !errors.Is(err, ownership.ErrNilOption) {
		t.Fatalf("NewFrozen with nil option = %v", err)
	}
}

// Detach transfers cleanup to the caller, so a configured Drop must not run.
func TestDetachSuppressesDrop(t *testing.T) {
	closer := &fakeCloser{}
	owner := ownership.NewCloser[*fakeCloser](closer)

	value, err := owner.Detach()
	if err != nil {
		t.Fatal(err)
	}
	if value != closer {
		t.Fatal("Detach returned a different value")
	}
	if closer.closed != 0 {
		t.Fatalf("Drop ran during Detach: closed=%d", closer.closed)
	}
	if err := owner.Release(); err != nil {
		t.Fatalf("Release after Detach = %v", err)
	}
	if closer.closed != 0 {
		t.Fatalf("Drop ran after Detach: closed=%d", closer.closed)
	}
}
