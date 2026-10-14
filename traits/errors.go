package traits

import (
	"errors"
	"fmt"
)

var (
	// ErrNilContext is what Future.Await returns for a nil context. A Future is
	// often carried by a caller that has no context of its own, and the panic a nil
	// ctx.Done() would produce is a poor answer to that.
	ErrNilContext = errors.New("traits: nil context")

	// ErrInvalidComposition identifies a trait composition that cannot run.
	ErrInvalidComposition = errors.New("invalid trait composition")
	// ErrNilTrait identifies a nil trait in a composition.
	ErrNilTrait = errors.New("nil trait")
)

// TraitError describes an invalid trait composition — a Drop or a Clone that
// cannot be composed. It is a different thing from ConfigError, which names
// a rejected option, and the two are not merged.
type TraitError struct {
	Trait string
	Index int
	Cause error
}

func (e *TraitError) Error() string {
	if e.Index < 0 {
		return fmt.Sprintf("compose %s: %v", e.Trait, e.Cause)
	}

	return fmt.Sprintf("compose %s: trait %d: %v", e.Trait, e.Index, e.Cause)
}

// Unwrap exposes both the general composition error and its specific cause.
func (e *TraitError) Unwrap() []error {
	return []error{ErrInvalidComposition, e.Cause}
}

func validate[T any](name string, traits []T, isNil func(T) bool) error {
	if len(traits) == 0 {
		return &TraitError{Trait: name, Index: -1, Cause: errors.New("no traits")}
	}

	for i, trait := range traits {
		if isNil(trait) {
			return &TraitError{Trait: name, Index: i, Cause: ErrNilTrait}
		}
	}

	return nil
}

// ErrInvalidConfig is the shared cause for a rejected option. Every package
// that takes one aliases it — `ownership.ErrInvalidConfig`,
// `dedupe.ErrInvalidConfig`, `pool.ErrInvalidConfig` — so a caller writes
// `errors.Is(err, <their package>.ErrInvalidConfig)` and gets the same value
// either way, and a caller matching this one directly gets one answer across
// all of them.
//
// The aliasing is the point, so a package that grows an option-taking
// constructor has to alias rather than declare: a second sentinel for the same
// condition is invisible to every check written against the first, and it fails
// only where nobody was looking. pool did exactly that, and the check that
// caught it is in pool's own test.
var ErrInvalidConfig = errors.New("invalid configuration")

// ConfigError reports a rejected option. One definition serves every package
// whose options are named, so "which option was refused" is answered the same
// way everywhere — including pool, where what is refused is a Config field
// rather than an element of an option list, and which is the same thing under
// a different spelling.
//
// It is deliberately not the same type as TraitError, which describes a
// composition failure rather than an option: they have different fields and
// different readers, and merging them would mean a type with an Index that is
// always -1 in one use and the trait position in the other.
type ConfigError struct {
	Option string
	Reason error
}

func (e *ConfigError) Error() string {
	return fmt.Sprintf("option %q: %v", e.Option, e.Reason)
}

func (e *ConfigError) Unwrap() []error { return []error{ErrInvalidConfig, e.Reason} }
