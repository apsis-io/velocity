package async_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/apsis-io/velocity/async"
)

var asyncSink []async.Outcome[int]
var asyncHookSink time.Duration

func BenchmarkGather(b *testing.B) {
	tasks := []async.Task[int]{
		{Run: func(context.Context) (int, error) { return 1, nil }},
		{Run: func(context.Context) (int, error) { return 2, nil }},
		{Run: func(context.Context) (int, error) { return 3, nil }},
		{Run: func(context.Context) (int, error) { return 4, nil }},
	}
	b.Run("no hooks", func(b *testing.B) {
		plan, _ := async.NewPlan(async.Limited(4), async.Hooks{}, tasks...)
		b.ReportAllocs()
		for b.Loop() {
			asyncSink, _ = async.Gather(context.Background(), plan)
		}
	})
	b.Run("task complete hook", func(b *testing.B) {
		plan, _ := async.NewPlan(async.Limited(4), async.Hooks{OnTaskComplete: func(_ int, _ string, waited, duration time.Duration, _ error) {
			asyncHookSink = waited + duration
		}}, tasks...)
		b.ReportAllocs()
		for b.Loop() {
			asyncSink, _ = async.Gather(context.Background(), plan)
		}
	})
}

// BenchmarkMapVersusGather runs the same function over the same collection
// both ways, to substantiate Map's fixed pool against Gather's one goroutine
// per task.
func BenchmarkMapVersusGather(b *testing.B) {
	square := func(_ context.Context, n int) (int, error) { return n * n, nil }
	for _, size := range []int{8, 1024} {
		items := make([]int, size)
		for i := range items {
			items[i] = i
		}
		b.Run(fmt.Sprintf("map/%d", size), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				asyncSink, _ = async.Map(context.Background(), async.Limited(8), async.Hooks{}, items, square)
			}
		})
		b.Run(fmt.Sprintf("gather/%d", size), func(b *testing.B) {
			tasks := make([]async.Task[int], size)
			for i, item := range items {
				tasks[i] = async.Task[int]{Run: func(ctx context.Context) (int, error) { return square(ctx, item) }}
			}
			plan, _ := async.NewPlan(async.Limited(8), async.Hooks{}, tasks...)
			b.ReportAllocs()
			for b.Loop() {
				asyncSink, _ = async.Gather(context.Background(), plan)
			}
		})
	}
}
