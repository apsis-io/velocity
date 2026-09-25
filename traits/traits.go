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
//
// **Who this is for, and who does not exist yet.** The obvious first caller is
// a caller that needs two Drops where the API takes one — `ownership.WithDrop`
// accepts a single `Drop`, so a cell with two things to release has to
// compose them here. No such caller exists in this repository, in either of
// the two projects that use velocity, or in the evaluation harness, which is
// worth saying rather than letting a reader assume the surface is exercised.
// **What would retire it:** anyone who finds this loop written by hand at two
// or more sites, or who composes drops inline without noticing they have.
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
//
// **Who this is for, and who does not exist yet.** A pipeline of copy steps
// where the intermediates own nothing — the case that needs no cleanup logic
// and is therefore the simplest thing this package could offer. Nothing in
// this repository, in periapsis or breeze, or in the evaluation harness
// transforms a value through two copy steps. **What would retire it:** a
// caller composing clones in a loop, which is eight lines and wrong in the
// same way every time it is written again.
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
//
// **Who this is for, and who does not exist yet.** The case this exists for is
// transforming an owned value through several steps where each intermediate is
// itself owned and must be released the moment it is superseded — encrypt then
// compress then write, say, where a dropped intermediate is a file descriptor
// or a temp directory. That case does not occur in this repository: the
// package doc advertises this form, and `ownership.Map` — which is the
// nearest thing here, chaining a Drop so the writer flushes before the file
// closes — takes a single `Clone` and a single `Drop`, so it composes neither.
// Nothing in periapsis or breeze or the evaluation harness composes clones
// either. **What would retire it:** a caller reaching for it and finding they
// wanted `WithDrop` with one `Drop` instead, which is the more likely outcome
// and the reason the honest description of this function is "nothing uses it
// yet" rather than "this is how you compose".
func (d Drop[T]) Clone(clones ...Clone[T]) (Clone[T], error) {
	if d == nil {
		return nil, &TraitError{Trait: "clones with drop", Index: 0, Cause: ErrNilTrait}
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
