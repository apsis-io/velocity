package pool

import (
	"errors"
	"fmt"
)

var (
	ErrInvalidConfig = errors.New("invalid pool configuration")
	ErrInvalidMax    = errors.New("pool max must be positive")
	ErrClosed        = errors.New("pool closed")
)

// ConfigError identifies the field that made a Config unusable.
type ConfigError struct {
	Field  string
	Reason error
}

func (e *ConfigError) Error() string   { return fmt.Sprintf("pool config %s: %v", e.Field, e.Reason) }
func (e *ConfigError) Unwrap() []error { return []error{ErrInvalidConfig, e.Reason} }
