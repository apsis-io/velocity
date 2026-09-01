package ownership

import "runtime"

// NewShared creates a shared handle with one reference.
func NewShared[T any](value T, opts ...Option[T]) (*Shared[T], error) {
	if len(opts) == 0 {
		return &Shared[T]{c: &cell[T]{value: value, mode: modeShared, shares: 1}}, nil
	}
	cfg, err := buildConfig(opts)
	if err != nil {
		return nil, err
	}
	return &Shared[T]{c: &cell[T]{value: value, mode: modeShared, shares: 1, drop: cfg.drop, clone: cfg.clone}}, nil
}

// Shared is one explicitly counted handle to a borrow-checked shared value. It
// must not be copied; use Clone to create another handle.
type Shared[T any] struct {
	_ noCopy
	c *cell[T]
	h handle
}

// State returns a synchronized ownership snapshot.
func (s *Shared[T]) State() State {
	if s == nil {
		return State{Released: true}
	}
	return s.c.stateFor(&s.h)
}

// Clone creates another explicitly counted Shared handle.
func (s *Shared[T]) Clone() (*Shared[T], error) {
	if s == nil || s.c == nil {
		return nil, &ReleasedError{Operation: OpClone}
	}
	c := s.c
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.checkHandle(&s.h, OpClone); err != nil {
		return nil, err
	}
	if c.mode != modeShared {
		return nil, &MovedError{Operation: OpClone}
	}
	c.shares++
	return &Shared[T]{c: c}, nil
}

// Borrow acquires an advanced shared read borrow.
func (s *Shared[T]) Borrow() (*ReadBorrow[T], error) {
	if s == nil || s.c == nil {
		return nil, &ReleasedError{Operation: OpBorrow}
	}
	lease, err := s.c.acquireRead(&s.h, modeShared)
	if err != nil {
		return nil, err
	}
	return newReadBorrow(lease), nil
}

// BorrowMut acquires an advanced exclusive mutable borrow.
func (s *Shared[T]) BorrowMut() (*WriteBorrow[T], error) {
	if s == nil || s.c == nil {
		return nil, &ReleasedError{Operation: OpBorrowMut}
	}
	lease, err := s.c.acquireWrite(&s.h, modeShared)
	if err != nil {
		return nil, err
	}
	return newWriteBorrow(lease), nil
}

// View runs fn against the value under a read borrow that lasts exactly as
// long as the call. See Owner.View, including the rule that the value must
// not outlive the call.
func (s *Shared[T]) View[R any](fn func(T) (R, error)) (R, error) {
	if s == nil {
		var zero R
		return zero, &ReleasedError{Operation: OpBorrow}
	}
	return scopedView(s.c, &s.h, modeShared, fn)
}

// Mutate runs fn with exclusive mutable access under a write borrow that
// lasts exactly as long as the call. See Owner.Mutate.
func (s *Shared[T]) Mutate[R any](fn func(*T) (R, error)) (R, error) {
	if s == nil {
		var zero R
		return zero, &ReleasedError{Operation: OpBorrowMut}
	}
	return scopedMutate(s.c, &s.h, modeShared, fn)
}

// WithRead is View for callbacks that report only an error.
func (s *Shared[T]) WithRead(fn func(T) error) error {
	_, err := s.View(errOnly(fn))
	return err
}

// WithWrite is Mutate for callbacks that report only an error.
func (s *Shared[T]) WithWrite(fn func(*T) error) error {
	_, err := s.Mutate(errOnly(fn))
	return err
}

// Snapshot clones the value under a temporary read borrow.
func (s *Shared[T]) Snapshot() (T, error) {
	if s == nil || s.c == nil {
		var zero T
		return zero, &ReleasedError{Operation: OpSnapshot}
	}
	s.c.mu.Lock()
	if err := s.c.checkHandle(&s.h, OpSnapshot); err != nil {
		s.c.mu.Unlock()
		var zero T
		return zero, err
	}
	clone := s.c.clone
	s.c.mu.Unlock()
	if clone == nil {
		var zero T
		return zero, &NoCloneError{Operation: OpSnapshot}
	}
	return s.View(clone)
}

// IntoOwner consumes the sole unborrowed Shared handle and returns a unique
// Owner. On conflict, it leaves the Shared handle unchanged.
func (s *Shared[T]) IntoOwner() (*Owner[T], error) {
	if s == nil || s.c == nil {
		return nil, &ReleasedError{Operation: OpIntoOwner}
	}
	c := s.c
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.checkHandle(&s.h, OpIntoOwner); err != nil {
		return nil, err
	}
	if c.mode != modeShared || c.shares != 1 || s.h.borrows != 0 || c.readers != 0 || c.writer {
		return nil, c.conflictLocked(OpIntoOwner)
	}
	s.h.state = handleMoved
	c.shares = 0
	c.mode = modeUnique
	return &Owner[T]{c: c}, nil
}

// Release gives up this Shared handle. The final release invokes Drop at most
// once. A handle with one of its own outstanding borrows cannot be released.
func (s *Shared[T]) Release() error {
	if s == nil || s.c == nil {
		return nil
	}
	c := s.c
	value, drop, first, err := c.beginCountedRelease(&s.h, modeShared)
	if err != nil {
		return err
	}
	if !first {
		return nil
	}
	var dropErr error
	if drop != nil {
		dropErr = drop(value)
	}
	c.finishDrop(dropErr)
	runtime.KeepAlive(s)
	return dropErr
}

// Close is an exact alias of Release.
func (s *Shared[T]) Close() error { return s.Release() }

// beginCountedRelease drives release for the reference-counted modes, Shared
// and Frozen, which differ only in the mode they expect.
func (c *cell[T]) beginCountedRelease(h *handle, expected mode) (value T, drop func(T) error, first bool, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if h.state == handleMoved || h.state == handleReleased || c.mode == modeReleased {
		return value, nil, false, nil
	}
	if h.borrows != 0 {
		return value, nil, false, c.conflictLocked(OpRelease)
	}
	if c.mode != expected {
		return value, nil, false, &MovedError{Operation: OpRelease}
	}
	if c.shares > 1 {
		h.state = handleReleased
		c.shares--
		return value, nil, false, nil
	}
	if c.readers != 0 || c.writer {
		return value, nil, false, c.conflictLocked(OpRelease)
	}
	h.state = handleReleased
	c.shares = 0
	value = c.value
	var zero T
	c.value = zero
	c.mode = modeReleased
	return value, c.drop, true, nil
}
