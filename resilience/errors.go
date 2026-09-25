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
	ErrInvalidBudget  = errors.New("invalid hedge budget")
	ErrNilFunction    = errors.New("nil resilience function")
	ErrNilClock       = errors.New("nil resilience clock")
	ErrNilTrip        = errors.New("nil breaker trip")
	// ErrNoBound is a policy that bounds nothing: no attempt count, and a
	// context with no deadline to inherit one from.
	ErrNoBound = errors.New("no retry bound: MaxAttempts is 0 and the context has no deadline")
	// ErrOpen is the rejection a Breaker returns instead of running a call.
	ErrOpen = errors.New("circuit breaker open")
	// ErrGaveUp reports that a policy stopped before its work succeeded,
	// whether the attempt count ran out or the context ended the loop. Every
	// give-up in this package satisfies it, so one question has one answer:
	//
	//	if errors.Is(err, resilience.ErrGaveUp) { ... }
	//
	// It exists because the alternative was a caller having to handle two
	// unrelated error types for one outcome, and which of the two it received
	// depending on arithmetic it did not write — see Retry.
	ErrGaveUp = errors.New("gave up before succeeding")
)

// PolicyError identifies invalid retry or breaker configuration.
type PolicyError struct{ Cause error }

func (e *PolicyError) Error() string   { return fmt.Sprintf("resilience policy: %v", e.Cause) }
func (e *PolicyError) Unwrap() []error { return []error{ErrInvalidPolicy, e.Cause} }

// RetryError is every give-up in this package: a retry whose attempts ran out,
// a retry or a condition whose context ended the loop, and a condition that
// never held within its bound. Attempts counts the calls that ran, and Last is
// the last failure or the context cause, absent when a condition simply never
// held.
//
// It is one type rather than one per policy so that errors.Is(err,
// ErrGaveUp) is the whole of the question a caller has to ask. The detail
// stays reachable: Unwrap yields both ErrGaveUp and Last, so a caller that
// wants the deadline specifically still finds it.
type RetryError struct {
	Attempts int
	Last     error
}

func (e *RetryError) Error() string {
	if e.Last == nil {
		return fmt.Sprintf("gave up after %d attempts", e.Attempts)
	}

	return fmt.Sprintf("gave up after %d attempts: %v", e.Attempts, e.Last)
}
func (e *RetryError) Unwrap() []error {
	if e.Last == nil {
		return []error{ErrGaveUp}
	}

	return []error{ErrGaveUp, e.Last}
}

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
