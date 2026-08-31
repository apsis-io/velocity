package traits

import "errors"

// Drop releases resources held by value. A Drop must return normally and must
// not panic or call runtime.Goexit.
type Drop[T any] func(value T) error

// Clone creates an independent copy of value. A Clone must return normally and
// must not panic or call runtime.Goexit.
type Clone[T any] func(value T) (T, error)

// ComposeDrops returns a Drop that invokes every input in registration order
// and joins all returned errors.
func ComposeDrops[T any](drops ...Drop[T]) (Drop[T], error) {
	if err := validate("drops", drops, func(drop Drop[T]) bool { return drop == nil }); err != nil {
		return nil, err
	}

	return func(value T) error {
		errs := make([]error, 0, len(drops))
		for _, drop := range drops {
			if err := drop(value); err != nil {
				errs = append(errs, err)
			}
		}
		return errors.Join(errs...)
	}, nil
}

// ComposeClones returns a Clone that applies every input sequentially and
// stops at the first error. It does not release discarded intermediate values;
// use Drop.Clone when T owns resources that need explicit cleanup.
func ComposeClones[T any](clones ...Clone[T]) (Clone[T], error) {
	if err := validate("clones", clones, func(clone Clone[T]) bool { return clone == nil }); err != nil {
		return nil, err
	}

	return func(value T) (T, error) {
		current := value
		for _, clone := range clones {
			var err error
			current, err = clone(current)
			if err != nil {
				var zero T
				return zero, err
			}
		}
		return current, nil
	}, nil
}

// Clone returns a sequential Clone that releases every owned intermediate as
// soon as it is superseded, using d. The caller's input and the final
// successful result are never dropped.
//
// If cloning fails, the current owned intermediate is dropped and both errors
// are joined. If dropping a superseded intermediate fails, the newly created
// value is also dropped and the operation stops.
func (d Drop[T]) Clone(clones ...Clone[T]) (Clone[T], error) {
	if d == nil {
		return nil, &ConfigError{Trait: "clones with drop", Index: 0, Cause: ErrNilTrait}
	}
	if err := validate("clones", clones, func(clone Clone[T]) bool { return clone == nil }); err != nil {
		return nil, err
	}

	return func(value T) (T, error) {
		current := value
		owned := false

		for _, clone := range clones {
			next, cloneErr := clone(current)
			if cloneErr != nil {
				if owned {
					cloneErr = errors.Join(cloneErr, d(current))
				}
				var zero T
				return zero, cloneErr
			}

			if owned {
				if dropErr := d(current); dropErr != nil {
					cleanupErr := d(next)
					var zero T
					return zero, errors.Join(dropErr, cleanupErr)
				}
			}
			current = next
			owned = true
		}

		return current, nil
	}, nil
}
