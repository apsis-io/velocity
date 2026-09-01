package ownership_test

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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
		_, _ = owner.Mutate(func(*int) (struct{}, error) {
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

// TestConcurrentModelNeverWaits drives one cell from many goroutines with
// every access and transfer operation the fuzz model uses, concurrently. The
// no-wait invariant says every operation returns promptly with either
// success or a lifecycle error, so the whole run must finish well inside the
// deadline, and the counters must be balanced once it does.
func TestConcurrentModelNeverWaits(t *testing.T) {
	const goroutines, ops = 8, 2000
	owner := mustOwner(t, 0)
	// Handles that goroutines share: the owner, and a shared and frozen
	// derivative produced on the fly by whichever goroutine gets there first.
	var shared atomic.Pointer[ownership.Shared[int]]
	var frozen atomic.Pointer[ownership.Frozen[int]]

	done := make(chan struct{})
	var wg sync.WaitGroup
	for g := range goroutines {
		wg.Go(func() {
			seed := uint64(g + 1)
			next := func() uint64 { seed = seed*6364136223846793005 + 1442695040888963407; return seed >> 33 }
			for range ops {
				var err error
				switch next() % 12 {
				case 0, 1:
					_, err = owner.View(func(v int) (int, error) { return v, nil })
				case 2:
					err = owner.WithWrite(func(v *int) error { *v++; return nil })
				case 3:
					var b *ownership.ReadBorrow[int]
					if b, err = owner.Borrow(); err == nil {
						_, err = b.Project(func(v int) (int, error) { return v, nil })
						_ = b.Release()
					}
				case 4:
					var b *ownership.WriteBorrow[int]
					if b, err = owner.BorrowMut(); err == nil {
						_, err = b.Update(func(v *int) (int, error) { *v++; return *v, nil })
						_ = b.Release()
					}
				case 5:
					_ = owner.State()
				case 6:
					// Try to share; only one goroutine can succeed, and only
					// while nothing is borrowed.
					if s, e := owner.IntoShared(); e == nil {
						shared.Store(s)
					} else {
						err = e
					}
				case 7:
					if s := shared.Load(); s != nil {
						if peer, e := s.Clone(); e == nil {
							_, err = peer.View(func(v int) (int, error) { return v, nil })
							_ = peer.Release()
						} else {
							err = e
						}
					}
				case 8:
					if s := shared.Load(); s != nil {
						err = s.WithWrite(func(v *int) error { *v++; return nil })
					}
				case 9:
					if f := frozen.Load(); f != nil {
						_, err = f.View(func(v int) (int, error) { return v, nil })
					}
				case 10:
					if s := shared.Load(); s != nil && shared.CompareAndSwap(s, nil) {
						// Thaw back to unique if we are the sole handle,
						// otherwise put it back for the others.
						if o, e := s.IntoOwner(); e == nil {
							if f, e := o.Freeze(); e == nil {
								frozen.Store(f)
							} else {
								err = e
								_ = o.Release()
							}
						} else {
							err = e
							shared.Store(s)
						}
					}
				case 11:
					if f := frozen.Load(); f != nil && frozen.CompareAndSwap(f, nil) {
						if o, e := f.IntoOwner(); e == nil {
							if s, e := o.IntoShared(); e == nil {
								shared.Store(s)
							} else {
								err = e
							}
						} else {
							err = e
							frozen.Store(f)
						}
					}
				}
				if err != nil && !knownLifecycleError(err) {
					t.Errorf("goroutine %d: unexpected error %v", g, err)
					return
				}
			}
		})
	}
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("concurrent model did not finish: an operation waited")
	}

	// Whichever handle owns the cell now, the borrow counters are balanced.
	var state ownership.State
	switch {
	case shared.Load() != nil:
		state = shared.Load().State()
	case frozen.Load() != nil:
		state = frozen.Load().State()
	default:
		state = owner.State()
	}
	if state.Readers != 0 || state.Writer {
		t.Fatalf("unbalanced state after run: %+v", state)
	}
}
