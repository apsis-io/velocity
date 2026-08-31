package resilience

import (
	"math"
	"math/rand/v2"
	"time"
)

// Classifier decides whether an error should be retried. Nil retries all errors.
type Classifier func(error) bool

// Backoff returns the delay before the specified one-based retry attempt.
type Backoff func(attempt int) time.Duration

// ExponentialBackoff returns capped exponential backoff with multiplicative
// jitter in [1-jitter, 1+jitter].
func ExponentialBackoff(base, max time.Duration, jitter float64) (Backoff, error) {
	if base < 0 || max < base || jitter < 0 || jitter > 1 {
		return nil, &PolicyError{Cause: ErrInvalidBackoff}
	}
	return func(attempt int) time.Duration {
		if attempt < 1 {
			return 0
		}
		shift := attempt - 1
		delay := base
		if shift >= 63 || delay > max>>shift {
			delay = max
		} else {
			delay <<= shift
			if delay > max {
				delay = max
			}
		}
		if jitter == 0 || delay == 0 {
			return delay
		}
		factor := 1 + (rand.Float64()*2-1)*jitter
		result := time.Duration(math.Round(float64(delay) * factor))
		if result > max {
			return max
		}
		return result
	}, nil
}
