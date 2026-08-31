package ownership_test

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/apsis-io/velocity/ownership"
)

func TestConcurrentReadersExcludeWriter(t *testing.T) {
	owner := mustOwner(t, 1)
	const readers = 32
	ready := make(chan struct{}, readers)
	release := make(chan struct{})
	errCh := make(chan error, readers)
	var wg sync.WaitGroup

	for range readers {
		wg.Go(func() {
			borrow, err := owner.Borrow()
			if err != nil {
				errCh <- err
				return
			}
			ready <- struct{}{}
			<-release
			errCh <- borrow.Release()
		})
	}
	for range readers {
		<-ready
	}
	if state := owner.State(); state.Readers != readers || state.Writer {
		t.Fatalf("state = %+v", state)
	}
	if _, err := owner.BorrowMut(); !errors.Is(err, ownership.ErrConflict) {
		t.Fatalf("BorrowMut = %v", err)
	}
	close(release)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestConcurrentWriteBorrowUpdateConflicts(t *testing.T) {
	owner := mustOwner(t, 0)
	borrow, err := owner.BorrowMut()
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	leave := make(chan struct{})
	first := make(chan error, 1)
	go func() {
		_, err := borrow.Update(func(value *int) (struct{}, error) {
			close(entered)
			<-leave
			*value++
			return struct{}{}, nil
		})
		first <- err
	}()
	<-entered
	if _, err := borrow.Update(func(value *int) (struct{}, error) {
		*value++
		return struct{}{}, nil
	}); !errors.Is(err, ownership.ErrConflict) {
		t.Fatalf("concurrent Update = %v", err)
	}
	close(leave)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if err := borrow.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentBorrowReleaseDoesNotLoseRelease(t *testing.T) {
	owner := mustOwner(t, 1)
	borrow, err := owner.Borrow()
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	leave := make(chan struct{})
	projected := make(chan error, 1)
	go func() {
		_, err := borrow.Project(func(int) (struct{}, error) {
			close(entered)
			<-leave
			return struct{}{}, nil
		})
		projected <- err
	}()
	<-entered
	if err := borrow.Release(); !errors.Is(err, ownership.ErrConflict) {
		t.Fatalf("Release during Project = %v", err)
	}
	close(leave)
	if err := <-projected; err != nil {
		t.Fatal(err)
	}
	if err := borrow.Release(); err != nil {
		t.Fatalf("Release after Project = %v", err)
	}
	if state := owner.State(); state.Readers != 0 {
		t.Fatalf("state = %+v", state)
	}
	if _, err := borrow.Project(func(int) (struct{}, error) { return struct{}{}, nil }); !errors.Is(err, ownership.ErrReleased) {
		t.Fatalf("Project after Release = %v", err)
	}
}

func TestConcurrentReleaseRunsDropOnce(t *testing.T) {
	var drops atomic.Int32
	owner := mustOwner(t, 1, ownership.WithDrop(func(int) error {
		drops.Add(1)
		return nil
	}))
	const callers = 64
	start := make(chan struct{})
	errCh := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Go(func() {
			<-start
			errCh <- owner.Release()
		})
	}
	close(start)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	if drops.Load() != 1 {
		t.Fatalf("drops = %d", drops.Load())
	}
}

func TestConcurrentSharedCloneRelease(t *testing.T) {
	var drops atomic.Int32
	owner := mustOwner(t, 1, ownership.WithDrop(func(int) error {
		drops.Add(1)
		return nil
	}))
	shared, err := owner.IntoShared()
	if err != nil {
		t.Fatal(err)
	}

	const workers = 64
	clones := make([]*ownership.Shared[int], workers)
	for i := range workers {
		clones[i], err = shared.Clone()
		if err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	errCh := make(chan error, workers)
	for _, clone := range clones {
		wg.Go(func() { errCh <- clone.Release() })
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := shared.Release(); err != nil {
		t.Fatal(err)
	}
	if drops.Load() != 1 {
		t.Fatalf("drops = %d", drops.Load())
	}
}

func TestCallbackPanicReleasesBorrow(t *testing.T) {
	owner := mustOwner(t, 1)
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("expected panic")
			}
		}()
		_, _ = owner.Write(func(access ownership.WriteAccess[int]) (struct{}, error) {
			panic("contract violation")
		})
	}()
	borrow, err := owner.BorrowMut()
	if err != nil {
		t.Fatalf("borrow after panic = %v", err)
	}
	if err := borrow.Release(); err != nil {
		t.Fatal(err)
	}
}
