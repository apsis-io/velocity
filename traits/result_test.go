package traits_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/apsis-io/velocity/traits"
)

// A Future is either unresolved or carries a Result, and Try is where that is
// visible without blocking.
func TestFutureTrySeparatesUnknownFromFailed(t *testing.T) {
	f := traits.NewFuture[int]()

	if _, ready := f.Try(); ready {
		t.Fatal("a new Future reported ready")
	}

	f.Complete(3, nil)

	res, ready := f.Try()
	if !ready || res.Value != 3 || !res.Ok() {
		t.Fatalf("Try = (%+v, %v), want a ready succeeded 3", res, ready)
	}

	// The three states are three different answers, which is the whole point:
	// unknown, succeeded, failed.
	failed := traits.NewFuture[string]()
	boom := errors.New("boom")
	failed.Complete("", boom)

	failedRes, ready := failed.Try()
	if !ready || failedRes.Ok() || !errors.Is(failedRes.Err, boom) {
		t.Fatalf("Try = (%+v, %v), want a ready failed", failedRes, ready)
	}
}

// Completing twice is a no-op rather than a corruption, because a worker
// finishing and a deferred cleanup racing to signal is an ordinary shape.
func TestFutureCompleteIsFirstWins(t *testing.T) {
	f := traits.NewFuture[int]()
	f.Complete(1, nil)
	f.Complete(2, errors.New("later"))

	res, _ := f.Try()
	if res.Value != 1 || !res.Ok() {
		t.Fatalf("Try = %+v, want the first completion", res)
	}
}

// The wait's error and the work's error are different facts, and a timeout
// says nothing about the outcome.
func TestFutureAwaitSeparatesTheWaitFromTheWork(t *testing.T) {
	f := traits.NewFuture[int]()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	if _, err := f.Await(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Await = %v, want the caller's deadline", err)
	}

	// The work completes afterwards, and the Future still reports it — the
	// timeout was about the wait, not about the work.
	boom := errors.New("boom")
	f.Complete(0, boom)

	res, err := f.Await(context.Background())

	// The returned error is the WORK's failure, wrapped, so the common
	// `if err != nil` catches it without reading the Result first — and it is
	// still the work's error underneath, not a replacement for it.
	if !errors.Is(err, boom) {
		t.Fatalf("Await = %v, want an error wrapping the work's failure", err)
	}

	if !errors.Is(res.Err, boom) {
		t.Fatalf("Result.Err = %v, want the work's failure", res.Err)
	}
}

// Concurrent completion is the case a Future is most likely to be built on, and
// the race detector is the only thing that checks it.
func TestFutureConcurrentCompletion(t *testing.T) {
	f := traits.NewFuture[int]()

	var wg sync.WaitGroup

	// Complete is guarded, so racing callers are safe; the race detector is what
	// checks that, which is the only thing here that cannot be asserted by
	// reading.
	for range 64 {
		wg.Go(func() { f.Complete(7, nil) })
	}

	wg.Wait()

	res, ready := f.Try()
	if !ready || res.Value != 7 {
		t.Fatalf("Try = (%+v, %v), want a ready 7", res, ready)
	}
}

// A nil Future is finished rather than pending, so a caller that loses one
// cannot wait on it forever.
func TestFutureNilIsFinished(t *testing.T) {
	var f *traits.Future[int]

	if _, ready := f.Try(); !ready {
		t.Fatal("a nil Future is not ready")
	}

	select {
	case <-f.Done():
	default:
		t.Fatal("a nil Future is not done")
	}

	// Completing one is a no-op rather than a panic.
	f.Complete(1, nil)

	// The zero Result is a succeeded zero, and that is the reading.
	res, err := f.Await(context.Background())
	if err != nil || !res.Ok() || res.Value != 0 {
		t.Fatalf("Await = (%+v, %v), want a finished zero Result", res, err)
	}
}

// A Result's zero value is a succeeded zero, deliberately: there is no "absent"
// state, so a map of Results aligned to what was requested cannot confuse one
// key for another.
func TestResultZeroIsASucceededZero(t *testing.T) {
	var res traits.Result[string]

	if !res.Ok() || res.Value != "" || res.Err != nil {
		t.Fatalf("the zero Result = %+v, want a succeeded zero", res)
	}
}
