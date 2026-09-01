package ownership

// Viewer is read capability over an owned T: what a function should ask for
// when it needs to look at a value and must not change it. Owner, Shared, and
// Frozen all satisfy it, so read-only intent is declared in the signature and
// enforced by the compiler without freezing the value first.
//
// Go interfaces cannot carry generic methods, so Viewer names WithRead rather
// than View; the package-level View projects through a Viewer.
type Viewer[T any] interface {
	WithRead(fn func(T) error) error
}

// Mutator is write capability over an owned T. Owner and Shared satisfy it;
// Frozen deliberately does not.
type Mutator[T any] interface {
	Viewer[T]
	WithWrite(fn func(*T) error) error
}

var (
	_ Mutator[int] = (*Owner[int])(nil)
	_ Mutator[int] = (*Shared[int])(nil)
	_ Viewer[int]  = (*Frozen[int])(nil)
)

// View projects a value out through any Viewer, restoring the (R, error)
// shape the interface cannot express. The lifetime rule is the same: R must
// not alias the value. A borrow failure returns the zero R; otherwise the
// result is whatever fn returned, error or not.
func View[T, R any](v Viewer[T], fn func(T) (R, error)) (R, error) {
	var result R
	if fn == nil {
		return result, &ProjectionError{Operation: OpProject}
	}
	if v == nil {
		return result, &ReleasedError{Operation: OpBorrow}
	}
	err := v.WithRead(func(value T) error {
		var err error
		result, err = fn(value)
		return err
	})
	return result, err
}

// Mutate is View for a Mutator. Like the method, it returns whatever fn
// returned alongside its error; mutations are not rolled back.
func Mutate[T, R any](m Mutator[T], fn func(*T) (R, error)) (R, error) {
	var result R
	if fn == nil {
		return result, &ProjectionError{Operation: OpUpdate}
	}
	if m == nil {
		return result, &ReleasedError{Operation: OpBorrowMut}
	}
	err := m.WithWrite(func(value *T) error {
		var err error
		result, err = fn(value)
		return err
	})
	return result, err
}
