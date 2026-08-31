package resilience

import (
	"context"
)

// Policy configures Retry. MaxAttempts must be positive.
type Policy struct {
	MaxAttempts int
	Retryable   Classifier
	Backoff     Backoff
	Clock       Clock
}

// Retry runs fn until success, a non-retryable error, the attempt limit, or
// context cancellation.
func Retry[T any](ctx context.Context, policy Policy, fn func(context.Context) (T, error)) (T, error) {
	var zero T
	if ctx == nil {
		return zero, &PolicyError{Cause: context.Canceled}
	}
	if fn == nil {
		return zero, &PolicyError{Cause: ErrNilFunction}
	}
	if policy.MaxAttempts <= 0 {
		return zero, &PolicyError{Cause: ErrInvalidPolicy}
	}
	clock := policy.Clock
	if clock == nil {
		clock = RealClock()
	}
	for attempt := 1; attempt <= policy.MaxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return zero, context.Cause(ctx)
		}
		value, err := fn(ctx)
		if err == nil {
			return value, nil
		}
		if policy.Retryable != nil && !policy.Retryable(err) {
			return zero, err
		}
		if attempt == policy.MaxAttempts {
			return zero, &RetryError{Attempts: attempt, Last: err}
		}
		if policy.Backoff == nil {
			continue
		}
		delay := policy.Backoff(attempt)
		if err := clock.Sleep(ctx, delay); err != nil {
			return zero, err
		}
	}
	return zero, &RetryError{Attempts: policy.MaxAttempts}
}
