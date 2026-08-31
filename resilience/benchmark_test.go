package resilience_test

import (
	"context"
	"testing"

	"github.com/apsis-io/velocity/resilience"
)

var resilienceSink int

func BenchmarkRetry(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		resilienceSink, _ = resilience.Retry(context.Background(), resilience.Policy{MaxAttempts: 1}, func(context.Context) (int, error) { return 1, nil })
	}
}
