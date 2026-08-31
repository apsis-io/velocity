package async_test

import (
	"context"
	"testing"

	"github.com/apsis-io/velocity/async"
)

var asyncSink []async.Outcome[int]

func BenchmarkGather(b *testing.B) {
	plan, _ := async.NewPlan(async.Limited(4),
		async.Task[int]{Run: func(context.Context) (int, error) { return 1, nil }},
		async.Task[int]{Run: func(context.Context) (int, error) { return 2, nil }},
		async.Task[int]{Run: func(context.Context) (int, error) { return 3, nil }},
		async.Task[int]{Run: func(context.Context) (int, error) { return 4, nil }},
	)
	b.ReportAllocs()
	for b.Loop() {
		asyncSink, _ = async.Gather(context.Background(), plan)
	}
}
