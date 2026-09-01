package async_test

import (
	"context"
	"errors"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/apsis-io/velocity/async"
	"github.com/apsis-io/velocity/ownership"
)

func TestMapPreservesInputOrderAndReportsFailuresByIndex(t *testing.T) {
	odd := errors.New("odd")
	got, err := runner(t, async.Limited(3)).Map(context.Background(), []int{1, 2, 3, 4, 5},
		func(_ context.Context, n int) (int, error) {
			time.Sleep(time.Duration(5-n) * time.Millisecond) // finish in reverse
			if n%2 == 1 {
				return -n, odd
			}
			return n * 10, nil
		})
	if !errors.Is(err, odd) {
		t.Fatalf("error = %v", err)
	}
	// Results are in input order; a failed item's slot is zero even though fn
	// returned -n beside its error.
	if !slices.Equal(got, []int{0, 20, 0, 40, 0}) {
		t.Fatalf("results = %v", got)
	}
	// The joined error carries one ItemError per failure, in index order
	// regardless of completion order.
	var failed []int
	for _, e := range joined(err) {
		var item *async.ItemError
		if !errors.As(e, &item) || !errors.Is(item, odd) {
			t.Fatalf("joined element %v is not an odd ItemError", e)
		}
		failed = append(failed, item.Index)
	}
	if !slices.Equal(failed, []int{0, 2, 4}) {
		t.Fatalf("failed indices = %v", failed)
	}
}

// joined splits an errors.Join result into its elements.
func joined(err error) []error {
	if u, ok := err.(interface{ Unwrap() []error }); ok {
		return u.Unwrap()
	}
	return []error{err}
}

func TestMapValidation(t *testing.T) {
	run := runner(t, async.Unlimited)
	if _, err := run.Map[int, int](context.Background(), []int{1}, nil); !errors.Is(err, async.ErrNilTask) {
		t.Fatalf("Map nil fn = %v", err)
	}
	if err := run.ForEach(context.Background(), []int{1}, nil); !errors.Is(err, async.ErrNilTask) {
		t.Fatalf("ForEach nil fn = %v", err)
	}
	var none *async.Runner
	if _, err := none.Map(context.Background(), []int{1}, func(context.Context, int) (int, error) { return 0, nil }); !errors.Is(err, async.ErrNilRunner) {
		t.Fatalf("nil Runner Map = %v", err)
	}
}

// An empty collection is a value, not a misconfiguration, so it is not an
// error the way an empty Plan is.
func TestMapEmptyCollectionIsNotAnError(t *testing.T) {
	called := false
	got, err := runner(t, async.Limited(1)).Map(context.Background(), []int(nil),
		func(context.Context, int) (int, error) { called = true; return 0, nil })
	if err != nil || len(got) != 0 || called {
		t.Fatalf("Map = (%v, %v), called=%t", got, err, called)
	}
	if err := runner(t, async.Limited(1)).ForEach(context.Background(), []int{}, func(context.Context, int) error { return nil }); err != nil {
		t.Fatal(err)
	}
}

// Map dispatches from a fixed pool, so goroutine count tracks the limit even
// for a collection far larger than it; Gather would spawn one per item.
func TestMapLimitBoundsWorkersAndGoroutines(t *testing.T) {
	const items = 2000
	const limit = 8

	var running, peak atomic.Int64
	release := make(chan struct{})
	list := make([]int, items)

	before := runtime.NumGoroutine()
	done := make(chan struct{})
	go func() {
		_, _ = runner(t, async.Limited(limit)).Map(context.Background(), list, func(context.Context, int) (int, error) {
			n := running.Add(1)
			for {
				old := peak.Load()
				if n <= old || peak.CompareAndSwap(old, n) {
					break
				}
			}
			<-release
			running.Add(-1)
			return 1, nil
		})
		close(done)
	}()

	time.Sleep(50 * time.Millisecond)
	growth := runtime.NumGoroutine() - before
	close(release)
	<-done

	if peak.Load() > limit {
		t.Fatalf("peak concurrent items = %d, want <= %d", peak.Load(), limit)
	}
	if growth > limit+2 {
		t.Fatalf("goroutine growth = %d for %d items at limit %d, want the pool size", growth, items, limit)
	}
}

func TestMapCancellationMarksUnstartedItems(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	var ran atomic.Int64

	var mu sync.Mutex
	hooked := map[int]error{}
	hooks := async.Hooks{OnTaskComplete: func(index int, _ string, _, _ time.Duration, err error) {
		mu.Lock()
		hooked[index] = err
		mu.Unlock()
	}}

	list := make([]int, 20)
	type result struct {
		results []int
		err     error
	}
	got := make(chan result, 1)
	go func() {
		results, err := runner(t, async.Limited(2), async.WithHooks(hooks)).Map(ctx, list, func(context.Context, int) (int, error) {
			once.Do(func() { close(started) })
			<-release
			ran.Add(1)
			return 1, nil
		})
		got <- result{results, err}
	}()

	<-started
	cancel()
	close(release)
	res := <-got

	if !errors.Is(res.err, context.Canceled) {
		t.Fatalf("Map = %v, want cancellation", res.err)
	}
	if len(res.results) != len(list) {
		t.Fatalf("results = %d, want %d", len(res.results), len(list))
	}
	// Claimed items ran to completion; unclaimed ones report the cause. Both
	// sets are contiguous, since claiming is a monotonic counter.
	completed := int(ran.Load())
	if completed == 0 || completed == len(list) {
		t.Fatalf("ran = %d of %d, expected a partial run", completed, len(list))
	}
	failed := map[int]error{}
	for _, e := range joined(res.err) {
		var item *async.ItemError
		if !errors.As(e, &item) {
			t.Fatalf("joined element %v is not an ItemError", e)
		}
		failed[item.Index] = item.Err
	}
	mu.Lock()
	defer mu.Unlock()
	for i := range list {
		if i < completed {
			if res.results[i] != 1 || failed[i] != nil {
				t.Fatalf("claimed item %d: result %d, err %v; want success", i, res.results[i], failed[i])
			}
		} else if !errors.Is(failed[i], context.Canceled) {
			t.Fatalf("unclaimed item %d = %v, want cancellation", i, failed[i])
		}
		if !errors.Is(hooked[i], failed[i]) {
			t.Fatalf("hook for %d reported %v, error has %v", i, hooked[i], failed[i])
		}
	}
	if len(hooked) != len(list) {
		t.Fatalf("hooks fired for %d items, want %d", len(hooked), len(list))
	}
}

// The reported wait must be the item's actual queueing delay: with one worker,
// the second item waits exactly as long as the first item runs.
func TestMapHooksReportQueueingDelay(t *testing.T) {
	const tolerance = 25 * time.Millisecond
	var mu sync.Mutex
	timings := map[int]struct{ wait, run time.Duration }{}
	hooks := async.Hooks{OnTaskComplete: func(index int, label string, wait, run time.Duration, _ error) {
		if label != "" {
			t.Errorf("label = %q, want empty", label)
		}
		mu.Lock()
		timings[index] = struct{ wait, run time.Duration }{wait, run}
		mu.Unlock()
	}}
	var blockStart, blockEnd time.Time
	mapStart := time.Now()
	_, err := runner(t, async.Limited(1), async.WithHooks(hooks)).Map(context.Background(), []int{0, 1}, func(_ context.Context, n int) (int, error) {
		if n == 0 {
			blockStart = time.Now()
			time.Sleep(20 * time.Millisecond)
			blockEnd = time.Now()
		}
		return n, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	wantRun := blockEnd.Sub(blockStart)
	if delta := timings[0].run - wantRun; delta < 0 || delta > tolerance {
		t.Fatalf("first run = %v, want close to %v", timings[0].run, wantRun)
	}
	wantWait := blockEnd.Sub(mapStart)
	if delta := timings[1].wait - wantWait; delta < -tolerance || delta > tolerance {
		t.Fatalf("second wait = %v, want close to %v", timings[1].wait, wantWait)
	}
	if timings[1].run > tolerance {
		t.Fatalf("second run = %v, want well under %v", timings[1].run, tolerance)
	}
}

func TestForEachJoinsErrors(t *testing.T) {
	bad := errors.New("bad")
	var seen atomic.Int64
	err := runner(t, async.Limited(2)).ForEach(context.Background(), []int{1, 2, 3}, func(_ context.Context, n int) error {
		seen.Add(int64(n))
		if n == 2 {
			return bad
		}
		return nil
	})
	var item *async.ItemError
	if !errors.Is(err, bad) || !errors.As(err, &item) || item.Index != 1 || seen.Load() != 6 {
		t.Fatalf("ForEach = %v, seen = %d", err, seen.Load())
	}
}

// Map inside View: the read borrow covers every worker because they all
// finish before Map returns, and a concurrent write is refused meanwhile.
func TestMapInsideViewHoldsTheBorrowAcrossWorkers(t *testing.T) {
	owner, err := ownership.New([]int{1, 2, 3, 4})
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Release()

	inFlight := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	type result struct {
		sum int
		err error
	}
	got := make(chan result, 1)
	go func() {
		sum, err := owner.View(func(items []int) (int, error) {
			squares, err := runner(t, async.Limited(2)).Map(context.Background(), items, func(_ context.Context, n int) (int, error) {
				once.Do(func() { close(inFlight) })
				<-release
				return n * n, nil
			})
			total := 0
			for _, square := range squares {
				total += square
			}
			return total, err
		})
		got <- result{sum, err}
	}()

	<-inFlight
	if err := owner.WithWrite(func(items *[]int) error { *items = nil; return nil }); !errors.Is(err, ownership.ErrConflict) {
		t.Fatalf("write during Map = %v, want ErrConflict", err)
	}
	close(release)
	if res := <-got; res.err != nil || res.sum != 30 {
		t.Fatalf("View = %+v", res)
	}
	if err := owner.WithWrite(func(items *[]int) error { *items = nil; return nil }); err != nil {
		t.Fatalf("write after Map = %v", err)
	}
}
