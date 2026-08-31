package async_test

import (
	"context"
	"errors"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/apsis-io/velocity/async"
	"github.com/apsis-io/velocity/ownership"
)

func plan[T any](t *testing.T, limit async.Limit, tasks ...async.Task[T]) async.Plan[T] {
	t.Helper()
	p, err := async.NewPlan(limit, tasks...)
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
			_, err := async.NewPlan(tt.limit, tt.tasks...)
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

func TestBroadcastUsesConcurrentReadAccess(t *testing.T) {
	owner, err := ownership.New(7)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Release()
	got, err := async.Broadcast(context.Background(), owner, async.Limited(2),
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
