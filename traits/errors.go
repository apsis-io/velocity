package traits

import (
	"errors"
	"fmt"
)

var (
	// ErrInvalidComposition identifies a trait composition that cannot run.
	ErrInvalidComposition = errors.New("invalid trait composition")
	// ErrNilTrait identifies a nil trait in a composition.
	ErrNilTrait = errors.New("nil trait")
)

// ConfigError describes an invalid trait composition.
type ConfigError struct {
	Trait string
	Index int
	Cause error
}

func (e *ConfigError) Error() string {
	if e.Index < 0 {
		return fmt.Sprintf("compose %s: %v", e.Trait, e.Cause)
	}
	return fmt.Sprintf("compose %s: trait %d: %v", e.Trait, e.Index, e.Cause)
}

// Unwrap exposes both the general composition error and its specific cause.
func (e *ConfigError) Unwrap() []error {
	return []error{ErrInvalidComposition, e.Cause}
}

func validate[T any](name string, traits []T, isNil func(T) bool) error {
	if len(traits) == 0 {
		return &ConfigError{Trait: name, Index: -1, Cause: errors.New("no traits")}
	}
	for i, trait := range traits {
		if isNil(trait) {
			return &ConfigError{Trait: name, Index: i, Cause: ErrNilTrait}
		}
	}
	return nil
}
