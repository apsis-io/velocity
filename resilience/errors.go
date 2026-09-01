package resilience

import (
	"errors"
	"fmt"
	"time"
)

var (
	ErrInvalidPolicy  = errors.New("invalid resilience policy")
	ErrInvalidBackoff = errors.New("invalid retry backoff")
	ErrInvalidBreaker = errors.New("invalid breaker policy")
	ErrNilFunction    = errors.New("nil resilience function")
	ErrNilClock       = errors.New("nil resilience clock")
	ErrNilTrip        = errors.New("nil breaker trip")
	// ErrOpen is the rejection a Breaker returns instead of running a call.
	ErrOpen = errors.New("circuit breaker open")
)

// PolicyError identifies invalid retry or breaker configuration.
type PolicyError struct{ Cause error }

func (e *PolicyError) Error() string   { return fmt.Sprintf("resilience policy: %v", e.Cause) }
func (e *PolicyError) Unwrap() []error { return []error{ErrInvalidPolicy, e.Cause} }

// RetryError preserves the final failure and number of attempts.
type RetryError struct {
	Attempts int
	Last     error
}

func (e *RetryError) Error() string {
	return fmt.Sprintf("retry failed after %d attempts: %v", e.Attempts, e.Last)
}
func (e *RetryError) Unwrap() error { return e.Last }

// BreakerError is a call the Breaker refused to make. State is Open, with
// RetryAfter the time until probes are admitted, or HalfOpen when every probe
// slot is taken, in which case RetryAfter is zero because the answer depends
// on the probes in flight rather than on the clock.
type BreakerError struct {
	State      State
	RetryAfter time.Duration
}

func (e *BreakerError) Error() string {
	if e.State == HalfOpen {
		return "circuit breaker half-open: probes in flight"
	}
	return fmt.Sprintf("circuit breaker open: retry after %v", e.RetryAfter)
}

func (e *BreakerError) Is(target error) bool { return target == ErrOpen }
