package dedupe

import (
	"errors"

	"github.com/apsis-io/velocity/traits"
)

var (
	ErrMissingResult = errors.New("dedupe: key missing from batch result")
	// ErrInvalidConfig is the shared cause behind a rejected option; see
	// traits.ErrInvalidConfig.
	ErrInvalidConfig      = traits.ErrInvalidConfig
	ErrNilContext         = errors.New("dedupe: nil context")
	ErrNilFunction        = errors.New("dedupe: nil function")
	ErrNilOwner           = errors.New("dedupe: nil owner")
	ErrNilOption          = errors.New("dedupe: nil option")
	ErrDuplicateOption    = errors.New("dedupe: duplicate option")
	ErrUnsupportedBackend = errors.New("dedupe: unsupported backend")
	// ErrOwnedResult rejects a plain-value call on a group whose results carry
	// a Drop or Clone; such a group serves results only through DoShared.
	ErrOwnedResult = errors.New("dedupe: owned results are served only through DoShared")
)

// ConfigError reports a rejected option. It is the shared type, so a caller
// matching on it does the same thing in every package with named options; the
// name stays here so existing errors.As calls keep compiling.
type ConfigError = traits.ConfigError
