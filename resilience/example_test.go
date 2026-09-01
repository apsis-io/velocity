package resilience_test

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/apsis-io/velocity/ownership"
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

// A hedge produces one result per attempt and returns one, so the losers
// must be disposed or they leak. When the result is an owned resource,
// Discard is its Drop — the case every other hedging library leaves to the
// caller to notice.
func ExampleHedge() {
	slow := make(chan struct{})
	policy := resilience.HedgePolicy[*ownership.Owner[string]]{
		MaxAttempts: 2,
		Delay:       func(int) time.Duration { return 5 * time.Millisecond },
		Discard: func(o *ownership.Owner[string]) error {
			return o.Release() // runs the loser's Drop
		},
	}
	winner, err := resilience.Hedge(context.Background(), policy,
		func(ctx context.Context, attempt int) (*ownership.Owner[string], error) {
			name := fmt.Sprintf("conn-%d", attempt)
			conn, _ := ownership.New(name, ownership.WithDrop(func(string) error {
				fmt.Println("closed", name)
				return nil
			}))
			if attempt == 0 {
				<-slow // overtaken by the hedge
			}
			return conn, nil
		})
	if err != nil {
		return
	}
	name, _ := winner.View(func(s string) (string, error) { return s, nil })
	fmt.Println("using", name)

	close(slow)
	// The loser's connection is closed by Discard once it arrives, so wait
	// for that before releasing ours.
	time.Sleep(50 * time.Millisecond)
	_ = winner.Release()
	// Output:
	// using conn-1
	// closed conn-0
	// closed conn-1
}
