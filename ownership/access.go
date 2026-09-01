package ownership

// scopedView runs fn against a shallow copy of the value under a read lease
// that lasts exactly as long as the call. It backs every View and WithRead.
func scopedView[T, R any](c *cell[T], h *handle, mode mode, fn func(T) (R, error)) (R, error) {
	if fn == nil {
		var zero R
		return zero, &ProjectionError{Operation: OpProject}
	}
	if c == nil {
		var zero R
		return zero, &ReleasedError{Operation: OpBorrow}
	}
	lease, err := c.acquireRead(h, mode)
	if err != nil {
		var zero R
		return zero, err
	}
	defer lease.closeScoped()
	c.mu.Lock()
	value := c.value
	c.mu.Unlock()
	return fn(value)
}

// scopedMutate runs fn with exclusive mutable access under a write lease that
// lasts exactly as long as the call. It backs every Mutate and WithWrite.
func scopedMutate[T, R any](c *cell[T], h *handle, mode mode, fn func(*T) (R, error)) (R, error) {
	if fn == nil {
		var zero R
		return zero, &ProjectionError{Operation: OpUpdate}
	}
	if c == nil {
		var zero R
		return zero, &ReleasedError{Operation: OpBorrowMut}
	}
	lease, err := c.acquireWrite(h, mode)
	if err != nil {
		var zero R
		return zero, err
	}
	defer lease.closeScoped()
	return lease.withWrite(OpUpdate, fn)
}

// errOnly adapts an error-only callback to the (R, error) shape.
func errOnly[T any](fn func(T) error) func(T) (struct{}, error) {
	if fn == nil {
		return nil
	}
	return func(value T) (struct{}, error) { return struct{}{}, fn(value) }
}
