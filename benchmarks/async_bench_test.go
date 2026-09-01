package benchmarks_test

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/aaronjan/hunch"
	"github.com/apsis-io/velocity/async"
	"github.com/sourcegraph/conc/iter"
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
		run, err := async.New(async.Unlimited)
		if err != nil {
			b.Fatal(err)
		}
		b.ReportAllocs()
		for b.Loop() {
			asyncVelocitySink, _ = run.Gather(ctx, velocityTasks...)
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

// BenchmarkAsyncMap compares homogeneous fan-out over a collection: velocity
// Map, conc iter.Mapper.MapErr, and a hand-rolled errgroup pool. All three use
// the same pool size and the same function; only velocity reports per-item
// outcomes rather than a bare result slice. conc receives *T where the others
// receive T, which for int is the same load.
func BenchmarkAsyncMap(b *testing.B) {
	ctx := context.Background()
	const workers = 8
	for _, size := range []int{8, 1024} {
		items := make([]int, size)
		for i := range items {
			items[i] = i
		}
		b.Run(fmt.Sprintf("velocity/map/%d", size), func(b *testing.B) {
			run, err := async.New(async.Limited(workers))
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			for b.Loop() {
				asyncErrgroupSink, _ = run.Map(ctx, items, benchmarkTask)
			}
		})
		b.Run(fmt.Sprintf("conc/iter.MapErr/%d", size), func(b *testing.B) {
			mapper := iter.Mapper[int, int]{MaxGoroutines: workers}
			b.ReportAllocs()
			for b.Loop() {
				asyncErrgroupSink, _ = mapper.MapErr(items, func(value *int) (int, error) { return benchmarkTask(ctx, *value) })
			}
		})
		b.Run(fmt.Sprintf("errgroup/pool/%d", size), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				results := make([]int, len(items))
				var next atomic.Int64
				var group errgroup.Group
				for range min(workers, len(items)) {
					group.Go(func() error {
						for {
							i := int(next.Add(1) - 1)
							if i >= len(items) {
								return nil
							}
							value, err := benchmarkTask(ctx, items[i])
							results[i] = value
							if err != nil {
								return err
							}
						}
					})
				}
				_ = group.Wait()
				asyncErrgroupSink = results
			}
		})
	}
}

func BenchmarkAsyncGatherVelocityLimited(b *testing.B) {
	ctx := context.Background()
	tasks := make([]async.Task[int], asyncTaskCount)
	for i := range asyncTaskCount {
		value := i
		tasks[i] = async.Task[int]{Run: func(context.Context) (int, error) { return value, nil }}
	}
	run, err := async.New(async.Limited(4))
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		asyncVelocitySink, _ = run.Gather(ctx, tasks...)
	}
}

// BenchmarkErrGroup compares first-error fan-out with the same eight
// functions: velocity ErrGroup versus x/sync/errgroup, both unlimited and
// both with a limit of 4. Neither returns values; that is Gather's job.
func BenchmarkErrGroup(b *testing.B) {
	ctx := context.Background()
	fns := make([]func(context.Context) error, asyncTaskCount)
	for i := range fns {
		fns[i] = func(ctx context.Context) error { _, err := benchmarkTask(ctx, i); return err }
	}
	for _, limit := range []int{0, 4} {
		name := "unlimited"
		if limit > 0 {
			name = fmt.Sprintf("limit-%d", limit)
		}
		b.Run("velocity/"+name, func(b *testing.B) {
			l := async.Unlimited
			if limit > 0 {
				l = async.Limited(limit)
			}
			run := async.Must(async.New(l))
			b.ReportAllocs()
			for b.Loop() {
				eg, gctx := run.ErrGroup(ctx)
				for _, fn := range fns {
					eg.Go(fn)
				}
				_ = gctx
				if err := eg.Wait(); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run("errgroup/"+name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				eg, gctx := errgroup.WithContext(ctx)
				if limit > 0 {
					eg.SetLimit(limit)
				}
				for _, fn := range fns {
					eg.Go(func() error { return fn(gctx) })
				}
				if err := eg.Wait(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
