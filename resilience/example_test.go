package resilience_test

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/apsis-io/velocity/resilience"
)

func ExampleRetry() {
	attempts := 0
	backoff, _ := resilience.ExponentialBackoff(0, 0, 0)
	value, err := resilience.Retry(context.Background(), resilience.Policy{
		MaxAttempts: 3,
		Backoff:     backoff,
	}, func(context.Context) (string, error) {
		attempts++
		if attempts < 2 {
			return "", errors.New("try again")
		}
		return "ok", nil
	})
	fmt.Println(value, err)
	// Output: ok <nil>
}

func ExampleBreaker() {
	breaker, _ := resilience.NewBreaker(resilience.BreakerPolicy{
		Trip:    resilience.ConsecutiveFailures(2),
		OpenFor: time.Minute,
		Failure: func(err error) bool { return !errors.Is(err, context.Canceled) },
	})
	unreachable := func(context.Context) (string, error) { return "", errors.New("connection refused") }

	for range 3 {
		_, err := breaker.Do(context.Background(), unreachable)
		fmt.Println(breaker.State(), errors.Is(err, resilience.ErrOpen))
	}
	// Output:
	// closed false
	// open false
	// open true
}

// A Breaker inside Retry: the retry loop stops on the breaker's rejection,
// which cannot change until the clock does, instead of burning attempts.
func ExampleBreaker_withRetry() {
	breaker, _ := resilience.NewBreaker(resilience.BreakerPolicy{
		Trip:    resilience.ConsecutiveFailures(2),
		OpenFor: time.Minute,
	})
	calls := 0
	_, err := resilience.Retry(context.Background(), resilience.Policy{
		MaxAttempts: 10,
		Retryable:   func(err error) bool { return !errors.Is(err, resilience.ErrOpen) },
	}, func(ctx context.Context) (string, error) {
		return breaker.Do(ctx, func(context.Context) (string, error) {
			calls++
			return "", errors.New("connection refused")
		})
	})
	fmt.Println(calls, errors.Is(err, resilience.ErrOpen))
	// Output: 2 true
}
