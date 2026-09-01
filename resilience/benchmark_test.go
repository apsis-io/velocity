package resilience_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/apsis-io/velocity/resilience"
)

var resilienceSink int

func BenchmarkRetry(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		resilienceSink, _ = resilience.Retry(context.Background(), resilience.Policy{MaxAttempts: 1}, func(context.Context) (int, error) { return 1, nil })
	}
}

func BenchmarkBreaker(b *testing.B) {
	breaker, err := resilience.NewBreaker(resilience.BreakerPolicy{
		Trip:    resilience.ConsecutiveFailures(1),
		OpenFor: time.Hour,
	})
	if err != nil {
		b.Fatal(err)
	}
	b.Run("closed", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			resilienceSink, _ = breaker.Do(context.Background(), func(context.Context) (int, error) { return 1, nil })
		}
	})
	b.Run("open", func(b *testing.B) {
		_, _ = breaker.Do(context.Background(), func(context.Context) (int, error) { return 0, errors.New("trip") })
		b.ReportAllocs()
		for b.Loop() {
			resilienceSink, _ = breaker.Do(context.Background(), func(context.Context) (int, error) { return 1, nil })
		}
	})
}
