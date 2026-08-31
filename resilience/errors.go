package resilience

import (
	"errors"
	"fmt"
)

var (
	ErrInvalidPolicy  = errors.New("invalid retry policy")
	ErrInvalidBackoff = errors.New("invalid retry backoff")
	ErrNilFunction    = errors.New("nil retry function")
	ErrNilClock       = errors.New("nil retry clock")
)

// PolicyError identifies invalid retry configuration.
type PolicyError struct{ Cause error }

func (e *PolicyError) Error() string   { return fmt.Sprintf("retry policy: %v", e.Cause) }
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
