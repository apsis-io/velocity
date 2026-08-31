package ownership_test

import (
	"errors"
	"testing"

	"github.com/apsis-io/velocity/ownership"
)

func TestScopedReadAccessClosesAfterEscapedAccessEnds(t *testing.T) {
	owner := mustOwner(t, 1)
	started := make(chan struct{})
	leave := make(chan struct{})
	done := make(chan error, 1)

	var escaped ownership.ReadAccess[int]
	if _, err := owner.Read(func(access ownership.ReadAccess[int]) (struct{}, error) {
		escaped = access
		go func() {
			_, err := access.Project(func(int) (struct{}, error) {
				close(started)
				<-leave
				return struct{}{}, nil
			})
			done <- err
		}()
		<-started
		return struct{}{}, nil
	}); err != nil {
		t.Fatal(err)
	}

	if state := owner.State(); state.Readers != 1 {
		t.Fatalf("state while escaped access is active = %+v", state)
	}
	close(leave)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if state := owner.State(); state.Readers != 0 {
		t.Fatalf("state after escaped access = %+v", state)
	}
	if err := owner.Release(); err != nil {
		t.Fatal(err)
	}
	if _, err := escaped.Project(func(value int) (int, error) { return value, nil }); !errors.Is(err, ownership.ErrReleased) {
		t.Fatalf("escaped Project after close = %v", err)
	}
}

func TestScopedWriteAccessClosesAfterEscapedAccessEnds(t *testing.T) {
	owner := mustOwner(t, 1)
	started := make(chan struct{})
	leave := make(chan struct{})
	done := make(chan error, 1)

	var escaped ownership.WriteAccess[int]
	if _, err := owner.Write(func(access ownership.WriteAccess[int]) (struct{}, error) {
		escaped = access
		go func() {
			_, err := access.Update(func(value *int) (struct{}, error) {
				close(started)
				<-leave
				*value++
				return struct{}{}, nil
			})
			done <- err
		}()
		<-started
		return struct{}{}, nil
	}); err != nil {
		t.Fatal(err)
	}

	if state := owner.State(); !state.Writer {
		t.Fatalf("state while escaped update is active = %+v", state)
	}
	if _, err := escaped.Update(func(value *int) (int, error) { return *value, nil }); !errors.Is(err, ownership.ErrReleased) {
		t.Fatalf("new Update after callback close = %v", err)
	}
	close(leave)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if state := owner.State(); state.Writer {
		t.Fatalf("state after escaped update = %+v", state)
	}
	value, err := owner.IntoValue()
	if err != nil || value != 2 {
		t.Fatalf("IntoValue = (%d, %v), want (2, nil)", value, err)
	}
}
