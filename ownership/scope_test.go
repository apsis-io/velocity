package ownership_test

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/apsis-io/velocity/ownership"
)

func TestScopeReleasesInReverseOrder(t *testing.T) {
	var order []string
	scope := ownership.NewScope()
	for _, name := range []string{"first", "second", "third"} {
		if err := scope.OnRelease(func() error {
			order = append(order, name)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	if scope.Len() != 3 {
		t.Fatalf("Len = %d", scope.Len())
	}
	if err := scope.Close(); err != nil {
		t.Fatal(err)
	}
	// Later resources are likely built on earlier ones, so they go first.
	if len(order) != 3 || order[0] != "third" || order[1] != "second" || order[2] != "first" {
		t.Fatalf("order = %v, want [third second first]", order)
	}
}

// One stubborn resource must not strand the rest.
func TestScopeContinuesPastFailuresAndJoinsErrors(t *testing.T) {
	firstErr := errors.New("first")
	thirdErr := errors.New("third")
	released := 0

	scope := ownership.NewScope()
	_ = scope.OnRelease(func() error { released++; return firstErr })
	_ = scope.OnRelease(func() error { released++; return nil })
	_ = scope.OnRelease(func() error { released++; return thirdErr })

	err := scope.Close()
	if released != 3 {
		t.Fatalf("released %d of 3", released)
	}
	if !errors.Is(err, firstErr) || !errors.Is(err, thirdErr) {
		t.Fatalf("Close = %v, want both errors", err)
	}
}

func TestScopeDisarmTransfersResponsibility(t *testing.T) {
	closer := &fakeCloser{}
	scope := ownership.NewScope()
	if err := scope.OwnCloser(closer); err != nil {
		t.Fatal(err)
	}

	if count := scope.Disarm(); count != 1 {
		t.Fatalf("Disarm = %d, want 1", count)
	}
	// The usual deferred Close must now be a no-op.
	if err := scope.Close(); err != nil {
		t.Fatal(err)
	}
	if closer.closed != 0 {
		t.Fatalf("disarmed scope still closed the resource: %d", closer.closed)
	}
}

func TestScopeCloseIsIdempotent(t *testing.T) {
	closer := &fakeCloser{}
	scope := ownership.NewScope()
	_ = scope.OwnCloser(closer)

	for range 3 {
		if err := scope.Close(); err != nil {
			t.Fatalf("Close = %v", err)
		}
	}
	if closer.closed != 1 {
		t.Fatalf("closed %d times, want 1", closer.closed)
	}
}

// A resource enrolled after the scope is finished would otherwise be silently
// forgotten, so enrolment must fail loudly and leave it with the caller.
func TestScopeRejectsEnrolmentAfterCloseOrDisarm(t *testing.T) {
	for _, tc := range []struct {
		name   string
		finish func(*ownership.Scope)
	}{
		{"closed", func(s *ownership.Scope) { _ = s.Close() }},
		{"disarmed", func(s *ownership.Scope) { s.Disarm() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scope := ownership.NewScope()
			tc.finish(scope)

			late := &fakeCloser{}
			err := scope.OwnCloser(late)
			if !errors.Is(err, ownership.ErrScopeClosed) {
				t.Fatalf("OwnCloser after finish = %v", err)
			}
			var scopeErr *ownership.ScopeError
			if !errors.As(err, &scopeErr) {
				t.Fatalf("error = %T, want *ScopeError", err)
			}
			if late.closed != 0 {
				t.Fatal("rejected resource was closed anyway")
			}
			if err := scope.OnRelease(func() error { return nil }); !errors.Is(err, ownership.ErrScopeClosed) {
				t.Fatalf("OnRelease after finish = %v", err)
			}
		})
	}
}

func TestScopeOwnMovesTheOwner(t *testing.T) {
	closer := &fakeCloser{}
	owner := ownership.NewCloser[*fakeCloser](closer)

	scope := ownership.NewScope()
	if err := scope.Own(owner); err != nil {
		t.Fatal(err)
	}
	// The caller's handle is spent; only the scope can release now.
	if state := owner.State(); !state.Moved {
		t.Fatalf("owner after Own = %+v", state)
	}
	if _, err := owner.Borrow(); !errors.Is(err, ownership.ErrMoved) {
		t.Fatalf("Borrow after Own = %v", err)
	}
	if err := owner.Release(); err != nil {
		t.Fatalf("Release of moved handle = %v", err)
	}
	if closer.closed != 0 {
		t.Fatal("moved-away handle ran Drop")
	}

	if err := scope.Close(); err != nil {
		t.Fatal(err)
	}
	if closer.closed != 1 {
		t.Fatalf("scope closed %d times, want 1", closer.closed)
	}
}

func TestScopeOwnRejectsBorrowedOwner(t *testing.T) {
	owner := mustOwner(t, 1)
	defer owner.Release()
	borrow, err := owner.Borrow()
	if err != nil {
		t.Fatal(err)
	}
	defer borrow.Release()

	scope := ownership.NewScope()
	if err := scope.Own(owner); !errors.Is(err, ownership.ErrConflict) {
		t.Fatalf("Own with outstanding borrow = %v", err)
	}
	// A rejected owner must not be enrolled.
	if scope.Len() != 0 {
		t.Fatalf("Len = %d, want 0", scope.Len())
	}
	if state := owner.State(); state.Moved {
		t.Fatal("rejected Own still moved the handle")
	}
}

func TestScopeNilInputs(t *testing.T) {
	scope := ownership.NewScope()
	if err := scope.OwnCloser(nil); !errors.Is(err, ownership.ErrReleased) {
		t.Fatalf("OwnCloser(nil) = %v", err)
	}
	if err := scope.OnRelease(nil); !errors.Is(err, ownership.ErrReleased) {
		t.Fatalf("OnRelease(nil) = %v", err)
	}
	if err := scope.Own[int](nil); !errors.Is(err, ownership.ErrReleased) {
		t.Fatalf("Own(nil) = %v", err)
	}
}

func TestScopeConcurrentEnrolment(t *testing.T) {
	scope := ownership.NewScope()
	var wg sync.WaitGroup
	for range 50 {
		wg.Go(func() { _ = scope.OnRelease(func() error { return nil }) })
	}
	wg.Wait()
	if scope.Len() != 50 {
		t.Fatalf("Len = %d, want 50", scope.Len())
	}
	if err := scope.Close(); err != nil {
		t.Fatal(err)
	}
}

// The cascade Scope exists to replace: each acquisition otherwise has to close
// everything opened before it.
func ExampleScope() {
	open := func(name string, fail bool) (*fakeCloser, error) {
		if fail {
			return nil, fmt.Errorf("dial %s", name)
		}
		return &fakeCloser{}, nil
	}

	build := func() (err error) {
		scope := ownership.NewScope()
		defer func() {
			if closeErr := scope.Close(); err == nil {
				err = closeErr
			}
		}()

		conn, err := open("conn", false)
		if err != nil {
			return err
		}
		_ = scope.OwnCloser(conn)

		raw, err := open("raw", false)
		if err != nil {
			return err
		}
		_ = scope.OwnCloser(raw)

		// This one fails, so the scope closes conn and raw on the way out.
		if _, err = open("sig", true); err != nil {
			return err
		}

		scope.Disarm()
		return nil
	}

	fmt.Println(build())
	// Output: dial sig
}
