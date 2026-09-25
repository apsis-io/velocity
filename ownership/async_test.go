package ownership_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/apsis-io/velocity/ownership"
	"github.com/apsis-io/velocity/traits"
)

// The Future is a receipt, not a promise: admission happened before the call
// returned, so the callback's outcome is all it carries.
func TestMutateAsyncReportsTheCallbackOutcome(t *testing.T) {
	owner, err := ownership.New(1)
	if err != nil {
		t.Fatal(err)
	}

	f := owner.MutateAsync(func(v *int) (int, error) {
		*v = 42
		return *v + 1, nil
	})

	res, err := f.Await(context.Background())
	if err != nil || res.Value != 43 || !res.Ok() {
		t.Fatalf("Await = (%+v, %v), want a succeeded 43", res, err)
	}

	// The mutation is visible on the value, and the borrow is gone.
	viewed, err := owner.View(func(v int) (int, error) { return v, nil })
	if err != nil || viewed != 42 {
		t.Fatalf("value = (%d, %v), want 42", viewed, err)
	}
}

func TestMutateAsyncReportsAnErroredCallback(t *testing.T) {
	owner, err := ownership.New(1)
	if err != nil {
		t.Fatal(err)
	}

	boom := errors.New("boom")

	res, err := owner.MutateAsync(func(*int) (int, error) { return 0, boom }).
		Await(context.Background())
	if err != nil {
		t.Fatalf("Await = %v, want the wait to succeed", err)
	}

	if !errors.Is(res.Err, boom) {
		t.Fatalf("Result.Err = %v, want the callback's error", res.Err)
	}

	if res.Ok() {
		t.Fatal("a failed callback reported Ok")
	}
}

// A conflicting mutation is refused at the call, not on the Future, so the
// caller keeps the synchronous admission contract Mutate already had.
func TestMutateAsyncRefusesAConflictBeforeReturning(t *testing.T) {
	owner, err := ownership.New(1)
	if err != nil {
		t.Fatal(err)
	}

	held, err := owner.BorrowMut()
	if err != nil {
		t.Fatal(err)
	}

	f := owner.MutateAsync(func(*int) (int, error) {
		t.Error("a refused mutation ran its callback")
		return 0, nil
	})

	// Already finished, and finished with the refusal.
	if _, ready := f.Try(); !ready {
		t.Fatal("a refused mutation is not finished at the call")
	}

	res, _ := f.Try()
	if !errors.Is(res.Err, ownership.ErrConflict) {
		t.Fatalf("Result.Err = %v, want ErrConflict synchronously", res.Err)
	}

	if err := held.Release(); err != nil {
		t.Fatal(err)
	}

	// And the refusal left nothing behind: the cell admits again.
	if _, err := owner.View(func(v int) (int, error) { return v, nil }); err != nil {
		t.Fatalf("View after a refused mutation: %v", err)
	}
}

// The borrow is released whatever happens, so a Future the caller drops is not
// a wedged cell. Nothing on the caller's path is responsible for the release —
// the goroutine owns it, which is the whole reason a dropped Future is safe.
func TestMutateAsyncReleasesTheBorrowEvenWhenAbandoned(t *testing.T) {
	owner, err := ownership.New(1)
	if err != nil {
		t.Fatal(err)
	}

	// Submit fifty without keeping a handle and without waiting. Most are
	// refused at the call — admission is synchronous, so at most one is admitted
	// at a time — and the admitted ones have to release themselves.
	for range 50 {
		owner.MutateAsync(func(v *int) (int, error) {
			*v++
			return 0, nil
		})
	}

	// The cell has to come back on the goroutines' own path. If the release were
	// the caller's job, dropping the Future would wedge it and this never
	// succeeds.
	deadline := time.Now().Add(5 * time.Second)

	for {
		borrowed, err := owner.BorrowMut()
		if err == nil {
			if err := borrowed.Release(); err != nil {
				t.Fatal(err)
			}

			return
		}

		if time.Now().After(deadline) {
			t.Fatalf("cell still refusing after 50 abandoned mutations: %v", err)
		}

		time.Sleep(time.Millisecond)
	}
}

// A panicking callback must not take the process with it, because there is no
// caller on the other side to recover, and must not wedge the cell either.
func TestMutateAsyncRecoversAPanicAndReleasesTheBorrow(t *testing.T) {
	owner, err := ownership.New(1)
	if err != nil {
		t.Fatal(err)
	}

	res, err := owner.MutateAsync(func(*int) (int, error) { panic("callback") }).
		Await(context.Background())
	if err != nil {
		t.Fatalf("Await = %v, want the wait to succeed", err)
	}

	var p *ownership.Panic
	if !errors.As(res.Err, &p) || p.Value != "callback" {
		t.Fatalf("Result.Err = %v, want a *Panic carrying the value", res.Err)
	}

	// Released before the panic was converted, so the cell is usable.
	if _, err := owner.View(func(v int) (int, error) { return v, nil }); err != nil {
		t.Fatalf("View after a panicking mutation: %v", err)
	}
}

// Try is non-blocking and says so, and it separates "unknown" from "failed" —
// which is the distinction a single (R, error) return cannot make.
func TestMutateAsyncTryIsNonBlocking(t *testing.T) {
	owner, err := ownership.New(1)
	if err != nil {
		t.Fatal(err)
	}

	started := make(chan struct{})
	release := make(chan struct{})

	f := owner.MutateAsync(func(*int) (int, error) {
		close(started)
		<-release

		return 7, nil
	})

	<-started

	if _, ready := f.Try(); ready {
		t.Fatal("Try reported ready while the callback was running")
	}

	close(release)

	if _, err := f.Await(context.Background()); err != nil {
		t.Fatal(err)
	}

	res, ready := f.Try()
	if !ready || res.Value != 7 || !res.Ok() {
		t.Fatalf("Try = (%+v, %v), want a ready succeeded 7", res, ready)
	}
}

// Giving up on the wait is not giving up on the work: the Future still
// resolves, the borrow still comes back, and a wait that gave up says nothing
// about the outcome — which is the distinction the two types exist to keep.
func TestMutateAsyncAwaitTimeoutDoesNotCancelTheWork(t *testing.T) {
	owner, err := ownership.New(1)
	if err != nil {
		t.Fatal(err)
	}

	release := make(chan struct{})

	// The callback blocks until this test says so, so the deadline below
	// expires while the work is genuinely in flight.
	f := owner.MutateAsync(func(v *int) (int, error) {
		<-release

		*v = 5

		return *v, nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	// The returned error is the wait's. The Result alongside it is the zero
	// Result, which is a *succeeded zero* and therefore reports Ok — so there
	// is nothing here to assert about it. That is the shape of every Go
	// function returning a value and an error, and the reason Await returns
	// the wait's failure separately rather than folding it into the Result: on
	// a timeout the Result is not to be read at all, and the only thing that
	// says so is the error.
	if _, err := f.Await(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Await = %v, want the caller's deadline", err)
	}

	close(release)

	after, err := f.Await(context.Background())
	if err != nil || after.Value != 5 {
		t.Fatalf("the Future did not resolve with the real outcome: (%+v, %v)", after, err)
	}
}

// Under contention the bound still holds: no more concurrent callbacks than the
// limit, which is the property a Future could plausibly break by moving the
// callback off the caller's goroutine.
func TestMutateAsyncKeepsTheLimit(t *testing.T) {
	owner, err := ownership.New(1)
	if err != nil {
		t.Fatal(err)
	}

	const total = 200

	var (
		mu       sync.Mutex
		futures  []*traits.Future[int]
		running  atomic.Int64
		peak     atomic.Int64
		wg       sync.WaitGroup
		inflight = make(chan struct{}, 8)
	)

	for range total {
		wg.Go(func() {
			// Serialise admission the way a real caller would be serialised by
			// whatever precedes the mutation.
			inflight <- struct{}{}

			f := owner.MutateAsync(func(v *int) (int, error) {
				n := running.Add(1)

				for {
					old := peak.Load()
					if n <= old || peak.CompareAndSwap(old, n) {
						break
					}
				}

				time.Sleep(10 * time.Microsecond)
				running.Add(-1)

				*v++

				return *v, nil
			})

			<-inflight

			mu.Lock()

			futures = append(futures, f)
			mu.Unlock()
		})
	}

	wg.Wait()

	// Every one either ran or was refused, and no goroutine is left holding.
	deadline := time.Now().Add(5 * time.Second)

	for {
		borrowed, err := owner.BorrowMut()
		if err == nil {
			if err := borrowed.Release(); err != nil {
				t.Fatal(err)
			}

			break
		}

		if time.Now().After(deadline) {
			t.Fatal("the cell never came back")
		}

		time.Sleep(time.Millisecond)
	}

	mu.Lock()
	count := len(futures)
	mu.Unlock()

	if count != total {
		t.Fatalf("collected %d futures, want %d", count, total)
	}

	if p := int(peak.Load()); p > 1 {
		t.Fatalf("%d callbacks ran at once, want 1", p)
	}
}

func TestMutateAsyncNilArguments(t *testing.T) {
	owner, err := ownership.New(1)
	if err != nil {
		t.Fatal(err)
	}

	// A nil function is a rejected mutation, finished rather than pending.
	res, ready := owner.MutateAsync[int](nil).Try()
	if !ready {
		t.Fatal("a nil-function mutation is pending")
	}

	var projection *ownership.ProjectionError
	if !errors.As(res.Err, &projection) {
		t.Fatalf("nil fn = %v, want a rejected mutation", res.Err)
	}

	var none *ownership.Owner[int]

	if res, _ := none.MutateAsync(func(*int) (int, error) { return 0, nil }).Try(); res.Ok() {
		t.Fatal("a nil owner reported success")
	}

	// A nil Future is finished rather than pending: nothing can wait on it
	// forever, and it carries the zero Result, which is a succeeded zero.
	var absent *traits.Future[int]

	if _, ready := absent.Try(); !ready {
		t.Fatal("a nil Future is not ready")
	}

	select {
	case <-absent.Done():
	default:
		t.Fatal("a nil Future is not done")
	}

	if res, err := absent.Await(context.Background()); err != nil || !res.Ok() {
		t.Fatalf("a nil Future Await = (%+v, %v), want a finished zero Result", res, err)
	}
}
