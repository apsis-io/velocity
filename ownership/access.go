package ownership

// ReadAccess is a callback-scoped read capability. Its methods fail after the
// callback's borrow is released.
type ReadAccess[T any] struct {
	lease *lease[T]
}

// Project invokes fn with a shallow copy of T.
func (a ReadAccess[T]) Project[R any](fn func(T) (R, error)) (R, error) {
	if fn == nil {
		var zero R
		return zero, &ProjectionError{Operation: OpProject}
	}
	if a.lease == nil {
		var zero R
		return zero, &ReleasedError{Operation: OpProject}
	}
	if err := a.lease.begin(borrowRead, OpProject); err != nil {
		var zero R
		return zero, err
	}
	defer a.lease.end()

	a.lease.cell.mu.Lock()
	value := a.lease.cell.value
	a.lease.cell.mu.Unlock()
	return fn(value)
}

// WriteAccess is a callback-scoped mutable capability. Its methods fail after
// the callback's borrow is released.
type WriteAccess[T any] struct {
	lease *lease[T]
}

// Update invokes fn with exclusive mutable access. Mutations are not rolled
// back when fn returns an error.
func (a WriteAccess[T]) Update[R any](fn func(*T) (R, error)) (R, error) {
	if fn == nil {
		var zero R
		return zero, &ProjectionError{Operation: OpUpdate}
	}
	if a.lease == nil {
		var zero R
		return zero, &ReleasedError{Operation: OpUpdate}
	}
	return a.lease.withWrite(OpUpdate, fn)
}
