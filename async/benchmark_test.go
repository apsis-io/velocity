package async_test

import (
	"context"
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
