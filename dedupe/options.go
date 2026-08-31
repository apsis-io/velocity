package dedupe

import (
	"fmt"

	"github.com/apsis-io/velocity/traits"
)

type backendKind uint8

const (
	backendMutex backendKind = iota
	backendXsync
	backendSharded
)

type config[K comparable, V any] struct {
	drop        traits.Drop[V]
	clone       traits.Clone[V]
	backendKind backendKind
	shards      int
	backendSet  bool
	hooks       Hooks[K]
	hooksSet    bool
}

// Option configures a Group and is sealed to this package.
type Option[K comparable, V any] interface {
	apply(*config[K, V]) error
}

type optionFunc[K comparable, V any] func(*config[K, V]) error

func (f optionFunc[K, V]) apply(cfg *config[K, V]) error { return f(cfg) }

// WithResultDrop configures cleanup of a successful round result.
func WithResultDrop[K comparable, V any](drop traits.Drop[V]) Option[K, V] {
	return optionFunc[K, V](func(cfg *config[K, V]) error {
		if drop == nil {
			return &ConfigError{Option: "result drop", Cause: ErrNilOption}
		}
		if cfg.drop != nil {
			return &ConfigError{Option: "result drop", Cause: ErrDuplicateOption}
		}
		cfg.drop = drop
		return nil
	})
}

// WithResultClone configures snapshots of successful round results.
func WithResultClone[K comparable, V any](clone traits.Clone[V]) Option[K, V] {
	return optionFunc[K, V](func(cfg *config[K, V]) error {
		if clone == nil {
			return &ConfigError{Option: "result clone", Cause: ErrNilOption}
		}
		if cfg.clone != nil {
			return &ConfigError{Option: "result clone", Cause: ErrDuplicateOption}
		}
		cfg.clone = clone
		return nil
	})
}

// WithHooks configures caller-supplied lifecycle instrumentation.
func WithHooks[K comparable, V any](hooks Hooks[K]) Option[K, V] {
	return optionFunc[K, V](func(cfg *config[K, V]) error {
		if cfg.hooksSet {
			return &ConfigError{Option: "hooks", Cause: ErrDuplicateOption}
		}
		cfg.hooks = hooks
		cfg.hooksSet = true
		return nil
	})
}

// WithMutexBackend selects the default mutex-protected map backend.
func WithMutexBackend[K comparable, V any]() Option[K, V] {
	return backendOption[K, V](backendMutex, 0)
}

// WithXsyncBackend selects the xsync map backend.
func WithXsyncBackend[K comparable, V any]() Option[K, V] {
	return backendOption[K, V](backendXsync, 0)
}

// WithSharded selects a mutex-map backend split across shards.
func WithSharded[K comparable, V any](shards int) Option[K, V] {
	return optionFunc[K, V](func(cfg *config[K, V]) error {
		if shards <= 0 {
			return &ConfigError{Option: "sharded backend", Cause: fmt.Errorf("shards must be positive: %d", shards)}
		}
		return setBackend(cfg, backendSharded, shards)
	})
}

func backendOption[K comparable, V any](kind backendKind, shards int) Option[K, V] {
	return optionFunc[K, V](func(cfg *config[K, V]) error { return setBackend(cfg, kind, shards) })
}

func setBackend[K comparable, V any](cfg *config[K, V], kind backendKind, shards int) error {
	if cfg.backendSet {
		return &ConfigError{Option: "backend", Cause: ErrDuplicateOption}
	}
	cfg.backendKind = kind
	cfg.shards = shards
	cfg.backendSet = true
	return nil
}
