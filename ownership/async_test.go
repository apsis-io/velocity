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

func TestMutateAsyncReportsTheCallbackOutcome(t *testing.T) {
	owner, err := ownership.New(1)
	if err != nil {
		t.Fatal(err)
	}

	f := owner.MutateAsync(context.Background(), func(v *int) (int, error) {
		*v = 42
		return *v + 1, nil
	})

	res, err := f.Await(context.Background())
	if err != nil || res.Value != 43 || !res.Ok() {
		t.Fatalf("Await = (%+v, %v), want a succeeded 43", res, err)
	}

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

	res, err := owner.MutateAsync(context.Background(), func(*int) (int, error) { return 0, boom }).
		Await(context.Background())
	if !errors.Is(err, boom) {
		t.Fatalf("Await = %v, want an error wrapping the callback's failure", err)
	}

	if !errors.Is(res.Err, boom) {
		t.Fatalf("Result.Err = %v, want the callback's error", res.Err)
	}

	if res.Ok() {
		t.Fatal("a failed callback reported Ok")
	}
}

// A contended mutation QUEUES. This is the contract the method exists for, and
// it is the opposite of Mutate, which refuses at once.
func TestMutateAsyncQueuesBehindAWriteBorrow(t *testing.T) {
	owner, err := ownership.New(1)
	if err != nil {
		t.Fatal(err)
	}

	held, err := owner.BorrowMut()
	if err != nil {
		t.Fatal(err)
	}

	ran := make(chan struct{})
	f := owner.MutateAsync(context.Background(), func(v *int) (int, error) {
		close(ran)

		*v = 7

		return *v, nil
	})

	// Queued, not refused and not running.
	if _, ready := f.Try(); ready {
		t.Fatal("a queued mutation reported ready")
	}

	select {
	case <-ran:
		t.Fatal("the callback ran while another write borrow was held")
	case <-time.After(50 * time.Millisecond):
	}

	// Releasing admits it.
	if err := held.Release(); err != nil {
		t.Fatal(err)
	}

	if _, err := f.Await(context.Background()); err != nil {
		t.Fatalf("Await = %v, want the queued mutation to succeed", err)
	}

	select {
	case <-ran:
	default:
		t.Fatal("the callback never ran after the borrow was released")
	}
}

// ctx bounds the wait for the borrow, so a caller that gives up cancels the
// mutation before it starts rather than leaving it queued.
func TestMutateAsyncCancelsWhileQueued(t *testing.T) {
	owner, err := ownership.New(1)
	if err != nil {
		t.Fatal(err)
	}

	held, err := owner.BorrowMut()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = held.Release() }()

	ran := atomic.Bool{}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	f := owner.MutateAsync(ctx, func(v *int) (int, error) {
		ran.Store(true)
		return 0, nil
	})

	res, err := f.Await(context.Background())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Await = %v, want the caller's deadline", err)
	}

	if ran.Load() {
		t.Fatal("a cancelled mutation still ran its callback")
	}

	if res.Ok() {
		t.Fatal("a cancelled mutation reported a succeeded zero")
	}
}

// The borrow is released whatever happens, so a Future the caller drops is not
// a wedged cell.
func TestMutateAsyncReleasesTheBorrowEvenWhenAbandoned(t *testing.T) {
	owner, err := ownership.New(1)
	if err != nil {
		t.Fatal(err)
	}

	// Submit fifty without keeping a handle. Every one queues and runs, so this
	// is also the queue draining behind an owner that never waits.
	for range 50 {
		owner.MutateAsync(context.Background(), func(v *int) (int, error) {
			*v++
			return 0, nil
		})
	}

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

// A panicking callback must not take the process with it — there is no caller on
// the other side to recover — and must not wedge the cell either.
func TestMutateAsyncRecoversAPanicAndReleasesTheBorrow(t *testing.T) {
	owner, err := ownership.New(1)
	if err != nil {
		t.Fatal(err)
	}

	res, err := owner.MutateAsync(context.Background(), func(*int) (int, error) { panic("callback") }).
		Await(context.Background())

	// A recovered panic arrives as the returned error too, and stays unwrappable
	// to the value underneath.
	var raised *ownership.Panic
	if !errors.As(err, &raised) || raised.Value != "callback" {
		t.Fatalf("Await = %v, want a *Panic carrying the value", err)
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

func TestMutateAsyncTryIsNonBlocking(t *testing.T) {
	owner, err := ownership.New(1)
	if err != nil {
		t.Fatal(err)
	}

	started := make(chan struct{})
	release := make(chan struct{})

	f := owner.MutateAsync(context.Background(), func(*int) (int, error) {
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

func TestMutateAsyncAwaitTimeoutDoesNotCancelTheWork(t *testing.T) {
	owner, err := ownership.New(1)
	if err != nil {
		t.Fatal(err)
	}

	release := make(chan struct{})

	// The callback blocks until this test says so, so the deadline below expires
	// while the work is genuinely in flight.
	f := owner.MutateAsync(context.Background(), func(v *int) (int, error) {
		<-release

		*v = 5

		return *v, nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	// The returned error is the wait's. The Result alongside it is the zero
	// Result, which is a succeeded zero and therefore reports Ok — so there is
	// nothing to assert about it. That is the shape of every Go function
	// returning a value and an error, and the reason Await returns the wait's
	// failure separately rather than folding it into the Result.
	if _, err := f.Await(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Await = %v, want the caller's deadline", err)
	}

	close(release)

	after, err := f.Await(context.Background())
	if err != nil || after.Value != 5 {
		t.Fatalf("the Future did not resolve with the real outcome: (%+v, %v)", after, err)
	}

	if !after.Ok() {
		t.Fatalf("the resolved Result reports a failure: %+v", after)
	}
}

// The queue must not weaken exclusion. Two hundred mutations submitted at once
// all land, and never more than one callback runs at a time.
func TestMutateAsyncQueuesWithoutWeakeningTheBound(t *testing.T) {
	// From zero, so the final value is exactly the number of mutations that ran.
	owner, err := ownership.New(0)
	if err != nil {
		t.Fatal(err)
	}

	const total = 200

	var (
		mu      sync.Mutex
		futures []*traits.Future[int]
		running atomic.Int64
		peak    atomic.Int64
		wg      sync.WaitGroup
	)

	for range total {
		wg.Go(func() {
			f := owner.MutateAsync(context.Background(), func(v *int) (int, error) {
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

			mu.Lock()

			futures = append(futures, f)
			mu.Unlock()
		})
	}

	wg.Wait()

	mu.Lock()
	collected := futures
	mu.Unlock()

	for _, f := range collected {
		if _, err := f.Await(context.Background()); err != nil {
			t.Fatalf("a queued mutation failed: %v", err)
		}
	}

	// Every one ran: the queue means a contended mutation is delayed, not lost.
	viewed, err := owner.View(func(v int) (int, error) { return v, nil })
	if err != nil || viewed != total {
		t.Fatalf("value = (%d, %v), want %d: a queued mutation was dropped", viewed, err, total)
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
	res, ready := owner.MutateAsync[int](context.Background(), nil).Try()
	if !ready {
		t.Fatal("a nil-function mutation is pending")
	}

	var projection *ownership.ProjectionError
	if !errors.As(res.Err, &projection) {
		t.Fatalf("nil fn = %v, want a rejected mutation", res.Err)
	}

	// A nil context is refused rather than dereferenced on the goroutine, where a
	// panic would take the process with it. The directive needs a blank line
	// above it, not a // line, or it joins the prose comment group and stops
	// applying.

	//lint:ignore SA1012 a nil context is exactly what is under test
	nilCtx := owner.MutateAsync[int](nil, func(*int) (int, error) { return 0, nil })

	if res, _ := nilCtx.Try(); !errors.Is(res.Err, traits.ErrNilContext) {
		t.Fatalf("nil ctx = %v, want traits.ErrNilContext", res.Err)
	}

	var none *ownership.Owner[int]

	if res, _ := none.MutateAsync(context.Background(), func(*int) (int, error) { return 0, nil }).
		Try(); res.Ok() {
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
}
