package async_test

import (
	"context"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/apsis-io/velocity/async"
	"github.com/apsis-io/velocity/ownership"
)

func plan[T any](t *testing.T, limit async.Limit, tasks ...async.Task[T]) async.Plan[T] {
	t.Helper()
	p, err := async.NewPlan(limit, async.Hooks{}, tasks...)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestNewPlanValidation(t *testing.T) {
	tests := []struct {
		name  string
		limit async.Limit
		tasks []async.Task[int]
		want  error
	}{
		{"unset limit", async.Limit{}, []async.Task[int]{{Run: func(context.Context) (int, error) { return 1, nil }}}, async.ErrInvalidLimit},
		{"zero limit", async.Limited(0), []async.Task[int]{{Run: func(context.Context) (int, error) { return 1, nil }}}, async.ErrInvalidLimit},
		{"empty", async.Unlimited, nil, async.ErrNoTasks},
		{"nil task", async.Unlimited, []async.Task[int]{{}}, async.ErrNilTask},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := async.NewPlan(tt.limit, async.Hooks{}, tt.tasks...)
			if !errors.Is(err, tt.want) {
				t.Fatalf("error = %v, want %v", err, tt.want)
			}
			var pe *async.PlanError
			if !errors.As(err, &pe) {
				t.Fatalf("error = %T, want PlanError", err)
			}
		})
	}
}

func TestGatherPreservesSourceOrderAndJoinsErrors(t *testing.T) {
	firstErr := errors.New("first")
	thirdErr := errors.New("third")
	p := plan(t, async.Unlimited,
		async.Task[int]{Label: "one", Run: func(context.Context) (int, error) { time.Sleep(3 * time.Millisecond); return 1, nil }},
		async.Task[int]{Label: "two", Run: func(context.Context) (int, error) { time.Sleep(time.Millisecond); return 0, firstErr }},
		async.Task[int]{Label: "three", Run: func(context.Context) (int, error) { return 3, thirdErr }},
	)
	got, err := async.Gather(context.Background(), p)
	if !errors.Is(err, firstErr) || !errors.Is(err, thirdErr) {
		t.Fatalf("error = %v", err)
	}
	if len(got) != 3 || got[0].Index != 0 || got[0].Value != 1 || got[1].Label != "two" || got[2].Err != thirdErr {
		t.Fatalf("outcomes = %+v", got)
	}
}

func TestGatherRespectsLimit(t *testing.T) {
	var active, max atomic.Int32
	p := plan(t, async.Limited(2),
		async.Task[int]{Run: func(context.Context) (int, error) {
			n := active.Add(1)
			for {
				old := max.Load()
				if n <= old || max.CompareAndSwap(old, n) {
					break
				}
			}
			time.Sleep(time.Millisecond)
			active.Add(-1)
			return 1, nil
		}},
		async.Task[int]{Run: func(context.Context) (int, error) {
			n := active.Add(1)
			for {
				old := max.Load()
				if n <= old || max.CompareAndSwap(old, n) {
					break
				}
			}
			time.Sleep(time.Millisecond)
			active.Add(-1)
			return 2, nil
		}},
		async.Task[int]{Run: func(context.Context) (int, error) {
			n := active.Add(1)
			for {
				old := max.Load()
				if n <= old || max.CompareAndSwap(old, n) {
					break
				}
			}
			time.Sleep(time.Millisecond)
			active.Add(-1)
			return 3, nil
		}},
	)
	if _, err := async.Gather(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	if got := max.Load(); got > 2 {
		t.Fatalf("max active = %d, want <= 2", got)
	}
}

func TestRaceReturnsFirstCompletion(t *testing.T) {
	p := plan(t, async.Unlimited,
		async.Task[int]{Label: "slow", Run: func(ctx context.Context) (int, error) { <-ctx.Done(); return 0, context.Cause(ctx) }},
		async.Task[int]{Label: "fast", Run: func(context.Context) (int, error) { return 2, nil }},
	)
	got, err := async.Race(context.Background(), p)
	if err != nil || got.Index != 1 || got.Value != 2 {
		t.Fatalf("Race = (%+v, %v)", got, err)
	}
}

func TestFirstSuccessSkipsErrors(t *testing.T) {
	first := errors.New("first")
	second := errors.New("second")
	p := plan(t, async.Unlimited,
		async.Task[int]{Run: func(context.Context) (int, error) { return 0, first }},
		async.Task[int]{Run: func(context.Context) (int, error) { return 2, nil }},
		async.Task[int]{Run: func(context.Context) (int, error) { return 0, second }},
	)
	got, err := async.FirstSuccess(context.Background(), p)
	if err != nil || got.Index != 1 || got.Value != 2 {
		t.Fatalf("FirstSuccess = (%+v, %v)", got, err)
	}
}

func TestFirstSuccessJoinsAllErrors(t *testing.T) {
	one := errors.New("one")
	two := errors.New("two")
	p := plan(t, async.Unlimited,
		async.Task[int]{Run: func(context.Context) (int, error) { return 0, one }},
		async.Task[int]{Run: func(context.Context) (int, error) { return 0, two }},
	)
	_, err := async.FirstSuccess(context.Background(), p)
	if !errors.Is(err, one) || !errors.Is(err, two) {
		t.Fatalf("error = %v", err)
	}
}

func TestPipelineIsTypedAndFailFast(t *testing.T) {
	pipeline := async.Start(func(context.Context) (int, error) { return 4, nil }).Then(func(context.Context, int) (string, error) {
		return "value", nil
	})
	got, err := pipeline.Run(context.Background())
	if err != nil || got != "value" {
		t.Fatalf("Run = (%q, %v)", got, err)
	}
	wantErr := errors.New("stop")
	called := false
	pipeline = async.Start(func(context.Context) (int, error) { return 1, wantErr }).Then(func(context.Context, int) (string, error) {
		called = true
		return "unexpected", nil
	})
	_, err = pipeline.Run(context.Background())
	if !errors.Is(err, wantErr) || called {
		t.Fatalf("Run = (%v), called=%t", err, called)
	}
}

func TestHooksReportPermitWaitAndRunDuration(t *testing.T) {
	var firstIndex atomic.Int32
	firstIndex.Store(-1)
	var mu sync.Mutex
	var events []struct {
		index int
		wait  time.Duration
		run   time.Duration
	}
	hooks := async.Hooks{OnTaskComplete: func(index int, _ string, wait, run time.Duration, _ error) {
		mu.Lock()
		events = append(events, struct {
			index int
			wait  time.Duration
			run   time.Duration
		}{index, wait, run})
		mu.Unlock()
	}}
	started := make(chan struct{})
	release := make(chan struct{})
	plan, err := async.NewPlan(async.Limited(1), hooks,
		async.Task[int]{Run: func(context.Context) (int, error) {
			if firstIndex.CompareAndSwap(-1, 0) {
				close(started)
				<-release
			}
			return 1, nil
		}},
		async.Task[int]{Run: func(context.Context) (int, error) {
			if firstIndex.CompareAndSwap(-1, 1) {
				close(started)
				<-release
			}
			return 2, nil
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		_, _ = async.Gather(context.Background(), plan)
		close(done)
	}()
	<-started
	time.Sleep(20 * time.Millisecond)
	close(release)
	<-done
	mu.Lock()
	defer mu.Unlock()
	if len(events) != 2 {
		t.Fatalf("events = %+v", events)
	}
	byIndex := map[int]struct{ wait, run time.Duration }{}
	for _, event := range events {
		byIndex[event.index] = struct{ wait, run time.Duration }{event.wait, event.run}
	}
	first := int(firstIndex.Load())
	second := 1 - first
	if byIndex[first].run < 15*time.Millisecond {
		t.Fatalf("first run duration = %v", byIndex[first].run)
	}
	if byIndex[second].wait < 15*time.Millisecond {
		t.Fatalf("second wait duration = %v", byIndex[second].wait)
	}
}

func TestHooksReportCancellationWhileWaitingForPermit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	release := make(chan struct{})
	var firstIndex atomic.Int32
	firstIndex.Store(-1)
	var mu sync.Mutex
	events := make(map[int]struct {
		wait time.Duration
		run  time.Duration
		err  error
	})
	hooks := async.Hooks{OnTaskComplete: func(index int, _ string, wait, run time.Duration, err error) {
		mu.Lock()
		events[index] = struct {
			wait time.Duration
			run  time.Duration
			err  error
		}{wait, run, err}
		mu.Unlock()
	}}
	plan, err := async.NewPlan(async.Limited(1), hooks,
		async.Task[int]{Run: func(context.Context) (int, error) {
			if firstIndex.CompareAndSwap(-1, 0) {
				close(started)
				<-release
			}
			return 1, nil
		}},
		async.Task[int]{Run: func(context.Context) (int, error) {
			if firstIndex.CompareAndSwap(-1, 1) {
				close(started)
				<-release
			}
			return 2, nil
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		_, _ = async.Gather(ctx, plan)
		close(done)
	}()
	<-started
	time.Sleep(10 * time.Millisecond)
	cancel()
	close(release)
	<-done
	mu.Lock()
	defer mu.Unlock()
	skipped := events[1-int(firstIndex.Load())]
	if skipped.wait == 0 || skipped.run != 0 || !errors.Is(skipped.err, context.Canceled) {
		t.Fatalf("skipped hook = %+v", skipped)
	}
}

func TestHooksAreZeroForUnlimitedPermitWait(t *testing.T) {
	var wait time.Duration
	plan, err := async.NewPlan(async.Unlimited, async.Hooks{OnTaskComplete: func(_ int, _ string, waited, _ time.Duration, _ error) { wait = waited }},
		async.Task[int]{Run: func(context.Context) (int, error) { return 1, nil }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := async.Gather(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	if wait != 0 {
		t.Fatalf("waited = %v, want zero", wait)
	}
}

func TestBroadcastUsesConcurrentReadAccess(t *testing.T) {
	owner, err := ownership.New(7)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Release()
	got, err := async.Broadcast(context.Background(), owner, async.Limited(2), async.Hooks{},
		func(context.Context, int) (int, error) { return 2, nil },
		func(context.Context, int) (int, error) { return 3, nil },
	)
	if err != nil || !slices.Equal([]int{got[0].Value, got[1].Value}, []int{2, 3}) {
		t.Fatalf("Broadcast = (%+v, %v)", got, err)
	}
}

func TestGroupCloseAndPanicPropagation(t *testing.T) {
	var group async.Group
	if err := group.Go(func() {}); err != nil {
		t.Fatal(err)
	}
	if err := group.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := group.Go(func() {}); !errors.Is(err, async.ErrClosed) {
		t.Fatalf("Go after Close = %v", err)
	}

	var panicGroup async.Group
	if err := panicGroup.Go(func() { panic("boom") }); err != nil {
		t.Fatal(err)
	}
	func() {
		defer func() {
			if recovered := recover(); recovered == nil {
				t.Fatal("Wait did not panic")
			}
		}()
		panicGroup.Wait()
	}()
}
