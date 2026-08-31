package ownership

import "errors"

// Map consumes this Owner and returns one over a value derived from it,
// preserving cleanup. It exists because IntoValue is otherwise the only exit
// from ownership and deliberately does not run Drop, so wrapping an owned
// resource by hand silently discards the original Drop.
//
// The derived Owner's Drop runs the one configured through opts first, then
// the source Owner's Drop against the retained source value. That order suits
// wrapping: flush the writer, then close the file underneath it.
//
// fn must not release, close, or otherwise consume the source value. The
// source Drop still runs later, so releasing it inside fn results in a double
// release. This is a caller obligation of the same kind as the rule that
// callbacks must return normally.
//
// If fn returns an error, this Owner is left untouched and remains usable.
func (o *Owner[T]) Map[U any](fn func(T) (U, error), opts ...Option[U]) (*Owner[U], error) {
	if fn == nil {
		return nil, &ProjectionError{Operation: OpMap}
	}
	if o == nil || o.c == nil {
		return nil, &ReleasedError{Operation: OpMap}
	}
	cfg, err := buildConfig(opts)
	if err != nil {
		return nil, err
	}

	// An exclusive lease reuses every precondition check and, unlike taking
	// the value outright, keeps the Owner recoverable if fn fails.
	c := o.c
	lease, err := c.acquireWrite(&o.h, modeUnique)
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	value := c.value
	sourceDrop := c.drop
	c.mu.Unlock()

	// fn runs with no mutex held, per the package invariant that user code
	// never runs under the cell lock, but with the lease still held so no
	// other borrow can begin.
	derived, err := fn(value)
	if err != nil {
		c.releaseLease(lease)
		return nil, err
	}

	c.mu.Lock()
	c.releaseLeaseLocked(lease)
	o.h.state = handleMoved
	var zero T
	c.value = zero
	c.mode = modeReleased
	c.mu.Unlock()

	return &Owner[U]{c: &cell[U]{
		value: derived,
		mode:  modeUnique,
		drop:  chainDrop(cfg.drop, sourceDrop, value),
		clone: cfg.clone,
	}}, nil
}

// chainDrop composes the derived value's Drop with the source's, closing over
// the source value so it survives until the derived Owner is released. It
// returns nil when neither exists, so a cell with no cleanup keeps none.
func chainDrop[T, U any](derived func(U) error, source func(T) error, sourceValue T) func(U) error {
	switch {
	case derived == nil && source == nil:
		return nil
	case source == nil:
		return derived
	case derived == nil:
		return func(U) error { return source(sourceValue) }
	default:
		return func(value U) error {
			return errors.Join(derived(value), source(sourceValue))
		}
	}
}
