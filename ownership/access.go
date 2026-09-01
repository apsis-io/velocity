package ownership

// scopedView runs fn against a shallow copy of the value under a read borrow
// that lasts exactly as long as the call. It backs every View and WithRead.
//
// No lease is allocated: a lease exists to give an advanced borrow an
// identity to release later, and a scoped borrow is released right here.
// The counters and checks are the same ones an advanced borrow uses.
func scopedView[T, R any](c *cell[T], h *handle, mode mode, fn func(T) (R, error)) (R, error) {
	if fn == nil {
		var zero R
		return zero, &ProjectionError{Operation: OpProject}
	}
	if c == nil {
		var zero R
		return zero, &ReleasedError{Operation: OpBorrow}
	}
	c.mu.Lock()
	if err := c.admitReadLocked(h, mode); err != nil {
		c.mu.Unlock()
		var zero R
		return zero, err
	}
	value := c.value
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.endReadLocked(h)
		c.mu.Unlock()
	}()
	return fn(value)
}

// scopedMutate runs fn with exclusive mutable access under a write borrow that
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
	c.mu.Lock()
	if err := c.admitWriteLocked(h, mode); err != nil {
		c.mu.Unlock()
		var zero R
		return zero, err
	}
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.endWriteLocked(h)
		c.mu.Unlock()
	}()
	// The writer flag excludes every other access until the deferred end,
	// so handing out the address is exclusive for exactly that long.
	return fn(&c.value)
}

// errOnly adapts an error-only callback to the (R, error) shape.
func errOnly[T any](fn func(T) error) func(T) (struct{}, error) {
	if fn == nil {
		return nil
	}
	return func(value T) (struct{}, error) { return struct{}{}, fn(value) }
}
