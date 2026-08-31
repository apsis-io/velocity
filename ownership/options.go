package ownership

import "github.com/apsis-io/velocity/traits"

type config[T any] struct {
	drop  traits.Drop[T]
	clone traits.Clone[T]
}

// Option configures an Owner and is sealed to this package.
type Option[T any] interface {
	apply(*config[T]) error
}

type optionFunc[T any] func(*config[T]) error

func (f optionFunc[T]) apply(cfg *config[T]) error { return f(cfg) }

// WithDrop configures the callback run at most once on explicit final release.
func WithDrop[T any](drop traits.Drop[T]) Option[T] {
	return optionFunc[T](func(cfg *config[T]) error {
		if drop == nil {
			return &ConfigError{Option: "drop", Reason: traits.ErrNilTrait}
		}
		if cfg.drop != nil {
			return &ConfigError{Option: "drop", Reason: ErrDuplicateOption}
		}
		cfg.drop = drop
		return nil
	})
}

// WithClone configures the callback used by Snapshot.
func WithClone[T any](clone traits.Clone[T]) Option[T] {
	return optionFunc[T](func(cfg *config[T]) error {
		if clone == nil {
			return &ConfigError{Option: "clone", Reason: traits.ErrNilTrait}
		}
		if cfg.clone != nil {
			return &ConfigError{Option: "clone", Reason: ErrDuplicateOption}
		}
		cfg.clone = clone
		return nil
	})
}
