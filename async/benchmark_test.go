package async_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/apsis-io/velocity/async"
)

var asyncSink []async.Outcome[int]
var asyncMapSink []int
var asyncHookSink time.Duration

func BenchmarkGather(b *testing.B) {
	tasks := []async.Task[int]{
		{Run: func(context.Context) (int, error) { return 1, nil }},
		{Run: func(context.Context) (int, error) { return 2, nil }},
		{Run: func(context.Context) (int, error) { return 3, nil }},
		{Run: func(context.Context) (int, error) { return 4, nil }},
	}
	b.Run("no hooks", func(b *testing.B) {
		run, _ := async.New(async.Limited(4))
		b.ReportAllocs()
		for b.Loop() {
			asyncSink, _ = run.Gather(context.Background(), tasks...)
		}
	})
	b.Run("task complete hook", func(b *testing.B) {
		run, _ := async.New(async.Limited(4), async.WithHooks(async.Hooks{OnTaskComplete: func(_ int, _ string, waited, duration time.Duration, _ error) {
			asyncHookSink = waited + duration
		}}))
		b.ReportAllocs()
		for b.Loop() {
			asyncSink, _ = run.Gather(context.Background(), tasks...)
		}
	})
}

// BenchmarkMapVersusGather runs the same function over the same collection
// both ways, to substantiate Map's fixed pool against Gather's one goroutine
// per task.
func BenchmarkMapVersusGather(b *testing.B) {
	square := func(_ context.Context, n int) (int, error) { return n * n, nil }
	run, _ := async.New(async.Limited(8))
	for _, size := range []int{8, 1024} {
		items := make([]int, size)
		for i := range items {
			items[i] = i
		}
		b.Run(fmt.Sprintf("map/%d", size), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				asyncMapSink, _ = run.Map(context.Background(), items, square)
			}
		})
		b.Run(fmt.Sprintf("gather/%d", size), func(b *testing.B) {
			tasks := make([]async.Task[int], size)
			for i, item := range items {
				tasks[i] = async.Task[int]{Run: func(ctx context.Context) (int, error) { return square(ctx, item) }}
			}
			b.ReportAllocs()
			for b.Loop() {
				asyncSink, _ = run.Gather(context.Background(), tasks...)
			}
		})
	}
}

// BenchmarkErrGroupGoVersusGoContext measures what bounding the permit wait
// costs a submitter whose permit is free, which is the case most submissions
// are. The contended case is where the documented ~250 ns applies, but it does
// not isolate in a benchmark: holding a permit needs a holder, and releasing it
// needs a timer, so the measurement is dominated by the holder rather than by
// the select.
func BenchmarkErrGroupGoVersusGoContext(b *testing.B) {
	work := func(context.Context) error { return nil }

	b.Run("Go", func(b *testing.B) {
		run, _ := async.New(async.Limited(8))
		b.ReportAllocs()
		for b.Loop() {
			eg, _ := run.ErrGroup(context.Background())
			eg.Go(work)
			_ = eg.Wait()
		}
	})
	b.Run("GoContext", func(b *testing.B) {
		run, _ := async.New(async.Limited(8))
		ctx := context.Background()
		b.ReportAllocs()
		for b.Loop() {
			eg, _ := run.ErrGroup(ctx)
			eg.GoContext(ctx, work)
			_ = eg.Wait()
		}
	})
}
