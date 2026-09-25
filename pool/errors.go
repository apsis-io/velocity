package pool

import (
	"errors"

	"github.com/apsis-io/velocity/traits"
)

var (
	// ErrInvalidConfig is the shared cause behind a rejected Config; see
	// traits.ErrInvalidConfig.
	ErrInvalidConfig = traits.ErrInvalidConfig

	ErrInvalidMax = errors.New("pool max must be positive")
	ErrClosed     = errors.New("pool closed")
)

// ConfigError names the field that made a Config unusable. It is the shared
// type, so a caller handling a rejected field matches one error rather than one
// per package.
//
// The field is called Option, which reads oddly for a struct literal, and the
// reason is that the thing refused is the same in both cases: a named thing the
// caller set and the package would not accept. The message loses the "pool
// config" prefix it used to carry, and the package is still named in every
// Reason — this package's reads "pool max must be positive" — so a log line
// does not lose the attribution.
type ConfigError = traits.ConfigError
