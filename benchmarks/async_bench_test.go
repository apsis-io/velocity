package benchmarks_test

import (
	"context"
	"testing"

	"github.com/aaronjan/hunch"
	"github.com/apsis-io/velocity/async"
	"golang.org/x/sync/errgroup"
)

const asyncTaskCount = 8

var (
	asyncVelocitySink []async.Outcome[int]
	asyncHunchSink    []any
	asyncErrgroupSink []int
)

func benchmarkTask(ctx context.Context, value int) (int, error) {
	return value, ctx.Err()
}

func BenchmarkAsyncGather(b *testing.B) {
	ctx := context.Background()

	velocityTasks := make([]async.Task[int], asyncTaskCount)
	hunchTasks := make([]hunch.Executable, asyncTaskCount)
	errgroupTasks := make([]func(context.Context) (int, error), asyncTaskCount)
	for i := range asyncTaskCount {
		value := i
		velocityTasks[i] = async.Task[int]{Run: func(ctx context.Context) (int, error) {
			return benchmarkTask(ctx, value)
		}}
		hunchTasks[i] = func(ctx context.Context) (any, error) {
			result, err := benchmarkTask(ctx, value)
			return result, err
		}
		errgroupTasks[i] = func(ctx context.Context) (int, error) {
			return benchmarkTask(ctx, value)
		}
	}

	b.Run("velocity/unlimited", func(b *testing.B) {
		plan, err := async.NewPlan(async.Unlimited, async.Hooks{}, velocityTasks...)
		if err != nil {
			b.Fatal(err)
		}
		b.ReportAllocs()
		for b.Loop() {
			asyncVelocitySink, _ = async.Gather(ctx, plan)
		}
	})

	b.Run("hunch/all", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			asyncHunchSink, _ = hunch.All(ctx, hunchTasks...)
		}
	})

	b.Run("errgroup/preallocated-source-order", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			results := make([]int, asyncTaskCount)
			var group errgroup.Group
			for i, task := range errgroupTasks {
				index, task := i, task
				group.Go(func() error {
					value, err := task(ctx)
					results[index] = value
					return err
				})
			}
			_ = group.Wait()
			asyncErrgroupSink = results
		}
	})
}

func BenchmarkAsyncGatherVelocityLimited(b *testing.B) {
	ctx := context.Background()
	tasks := make([]async.Task[int], asyncTaskCount)
	for i := range asyncTaskCount {
		value := i
		tasks[i] = async.Task[int]{Run: func(context.Context) (int, error) { return value, nil }}
	}
	plan, err := async.NewPlan(async.Limited(4), async.Hooks{}, tasks...)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		asyncVelocitySink, _ = async.Gather(ctx, plan)
	}
}
