package async_test

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/apsis-io/velocity/async"
	"github.com/apsis-io/velocity/ownership"
)

func TestMapPreservesInputOrderAndJoinsErrors(t *testing.T) {
	odd := errors.New("odd")
	got, err := async.Map(context.Background(), async.Limited(3), async.Hooks{}, []int{1, 2, 3, 4, 5},
		func(_ context.Context, n int) (int, error) {
			time.Sleep(time.Duration(5-n) * time.Millisecond) // finish in reverse
			if n%2 == 1 {
				return 0, odd
			}
			return n * 10, nil
		})
	if !errors.Is(err, odd) {
		t.Fatalf("error = %v", err)
	}
	if len(got) != 5 {
		t.Fatalf("outcomes = %d", len(got))
	}
	for i, outcome := range got {
		if outcome.Index != i {
			t.Fatalf("outcome %d has index %d", i, outcome.Index)
		}
		n := i + 1
		if n%2 == 1 && !errors.Is(outcome.Err, odd) {
			t.Fatalf("outcome %d = %+v, want odd", i, outcome)
		}
		if n%2 == 0 && (outcome.Err != nil || outcome.Value != n*10) {
			t.Fatalf("outcome %d = %+v, want %d", i, outcome, n*10)
		}
	}
}

func TestMapValidation(t *testing.T) {
	tests := []struct {
		name  string
		limit async.Limit
		fn    func(context.Context, int) (int, error)
		want  error
	}{
		{"unset limit", async.Limit{}, func(context.Context, int) (int, error) { return 0, nil }, async.ErrInvalidLimit},
		{"zero limit", async.Limited(0), func(context.Context, int) (int, error) { return 0, nil }, async.ErrInvalidLimit},
		{"nil fn", async.Unlimited, nil, async.ErrNilTask},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := async.Map(context.Background(), tt.limit, async.Hooks{}, []int{1}, tt.fn)
			if !errors.Is(err, tt.want) {
				t.Fatalf("error = %v, want %v", err, tt.want)
			}
		})
	}
	if err := async.ForEach(context.Background(), async.Unlimited, async.Hooks{}, []int{1}, nil); !errors.Is(err, async.ErrNilTask) {
		t.Fatalf("ForEach nil fn = %v", err)
	}
}

// An empty collection is a value, not a misconfiguration, so it is not an
// error the way an empty Plan is.
func TestMapEmptyCollectionIsNotAnError(t *testing.T) {
	called := false
	got, err := async.Map(context.Background(), async.Limited(1), async.Hooks{}, []int(nil),
		func(context.Context, int) (int, error) { called = true; return 0, nil })
	if err != nil || len(got) != 0 || called {
		t.Fatalf("Map = (%v, %v), called=%t", got, err, called)
	}
	if err := async.ForEach(context.Background(), async.Limited(1), async.Hooks{}, []int{}, func(context.Context, int) error { return nil }); err != nil {
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
		_, _ = async.Map(context.Background(), async.Limited(limit), async.Hooks{}, list, func(context.Context, int) (int, error) {
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
		outcomes []async.Outcome[int]
		err      error
	}
	got := make(chan result, 1)
	go func() {
		outcomes, err := async.Map(ctx, async.Limited(2), hooks, list, func(context.Context, int) (int, error) {
			once.Do(func() { close(started) })
			<-release
			ran.Add(1)
			return 1, nil
		})
		got <- result{outcomes, err}
	}()

	<-started
	cancel()
	close(release)
	res := <-got

	if !errors.Is(res.err, context.Canceled) {
		t.Fatalf("Map = %v, want cancellation", res.err)
	}
	if len(res.outcomes) != len(list) {
		t.Fatalf("outcomes = %d, want %d", len(res.outcomes), len(list))
	}
	// Claimed items ran to completion; unclaimed ones report the cause. Both
	// sets are contiguous, since claiming is a monotonic counter.
	completed := int(ran.Load())
	if completed == 0 || completed == len(list) {
		t.Fatalf("ran = %d of %d, expected a partial run", completed, len(list))
	}
	mu.Lock()
	defer mu.Unlock()
	for i, outcome := range res.outcomes {
		if outcome.Index != i {
			t.Fatalf("outcome %d has index %d", i, outcome.Index)
		}
		if i < completed && outcome.Err != nil {
			t.Fatalf("claimed outcome %d = %+v, want success", i, outcome)
		}
		if i >= completed && !errors.Is(outcome.Err, context.Canceled) {
			t.Fatalf("unclaimed outcome %d = %+v, want cancellation", i, outcome)
		}
		if !errors.Is(hooked[i], outcome.Err) {
			t.Fatalf("hook for %d reported %v, outcome has %v", i, hooked[i], outcome.Err)
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
	_, err := async.Map(context.Background(), async.Limited(1), hooks, []int{0, 1}, func(_ context.Context, n int) (int, error) {
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
	err := async.ForEach(context.Background(), async.Limited(2), async.Hooks{}, []int{1, 2, 3}, func(_ context.Context, n int) error {
		seen.Add(int64(n))
		if n == 2 {
			return bad
		}
		return nil
	})
	if !errors.Is(err, bad) || seen.Load() != 6 {
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
			outcomes, err := async.Map(context.Background(), async.Limited(2), async.Hooks{}, items, func(_ context.Context, n int) (int, error) {
				once.Do(func() { close(inFlight) })
				<-release
				return n * n, nil
			})
			total := 0
			for _, outcome := range outcomes {
				total += outcome.Value
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
