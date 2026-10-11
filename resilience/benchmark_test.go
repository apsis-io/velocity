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

func BenchmarkHedgePrimaryWins(b *testing.B) {
	// Delay longer than the bench: no hedge ever fires, so this measures
	// the machinery a fast backend pays for the option to hedge.
	policy := resilience.HedgePolicy[int]{
		MaxAttempts: 3,
		Delay:       func(int) time.Duration { return time.Hour },
	}

	b.ReportAllocs()

	for b.Loop() {
		resilienceSink, _ = resilience.Hedge(context.Background(), policy, func(context.Context, int) (int, error) {
			return 1, nil
		})
	}
}

func BenchmarkHedgeRaced(b *testing.B) {
	// The primary is slow; the hedge fires at 1ms and wins. One result is
	// discarded per iteration — the shape Hedge exists for.
	policy := resilience.HedgePolicy[int]{
		MaxAttempts: 2,
		Delay:       func(int) time.Duration { return time.Millisecond },
	}

	b.ReportAllocs()

	for b.Loop() {
		resilienceSink, _ = resilience.Hedge(context.Background(), policy, func(_ context.Context, attempt int) (int, error) {
			if attempt == 0 {
				time.Sleep(3 * time.Millisecond)
			}

			return 1, nil
		})
	}
}
