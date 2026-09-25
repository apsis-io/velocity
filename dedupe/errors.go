package dedupe

import "errors"
import "fmt"

var (
	ErrMissingResult      = errors.New("dedupe: key missing from batch result")
	ErrInvalidConfig      = errors.New("dedupe: invalid configuration")
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

type ConfigError struct {
	Option string
	Cause  error
}

func (e *ConfigError) Error() string   { return fmt.Sprintf("dedupe option %q: %v", e.Option, e.Cause) }
func (e *ConfigError) Unwrap() []error { return []error{ErrInvalidConfig, e.Cause} }
