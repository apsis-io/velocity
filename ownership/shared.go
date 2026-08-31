package ownership

import "runtime"

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

// Read runs fn under a callback-scoped shared read borrow.
func (s *Shared[T]) Read[R any](fn func(ReadAccess[T]) (R, error)) (R, error) {
	if fn == nil {
		var zero R
		return zero, &ProjectionError{Operation: OpProject}
	}
	borrow, err := s.Borrow()
	if err != nil {
		var zero R
		return zero, err
	}
	defer borrow.Release()
	return fn(ReadAccess[T]{lease: borrow.lease})
}

// Write runs fn under a callback-scoped exclusive mutable borrow.
func (s *Shared[T]) Write[R any](fn func(WriteAccess[T]) (R, error)) (R, error) {
	if fn == nil {
		var zero R
		return zero, &ProjectionError{Operation: OpUpdate}
	}
	borrow, err := s.BorrowMut()
	if err != nil {
		var zero R
		return zero, err
	}
	defer borrow.Release()
	return fn(WriteAccess[T]{lease: borrow.lease})
}

// Snapshot clones the value under a temporary read borrow.
func (s *Shared[T]) Snapshot() (T, error) {
	if s == nil || s.c == nil {
		var zero T
		return zero, &ReleasedError{Operation: OpSnapshot}
	}
	s.c.mu.Lock()
	clone := s.c.clone
	s.c.mu.Unlock()
	if clone == nil {
		var zero T
		return zero, &NoCloneError{Operation: OpSnapshot}
	}
	return s.Read(func(access ReadAccess[T]) (T, error) {
		return access.Project(clone)
	})
}

// TryUnwrap consumes the sole unborrowed Shared handle and returns a unique
// Owner. On conflict, it leaves the Shared handle unchanged.
func (s *Shared[T]) TryUnwrap() (*Owner[T], error) {
	if s == nil || s.c == nil {
		return nil, &ReleasedError{Operation: OpTryUnwrap}
	}
	c := s.c
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.checkHandle(&s.h, OpTryUnwrap); err != nil {
		return nil, err
	}
	if c.mode != modeShared || c.shares != 1 || s.h.borrows != 0 || c.readers != 0 || c.writer {
		return nil, c.conflict(OpTryUnwrap)
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
	value, drop, wait, first, err := c.beginSharedRelease(&s.h)
	if err != nil {
		return err
	}
	if wait != nil {
		<-wait
		return nil
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

func (c *cell[T]) beginSharedRelease(h *handle) (value T, drop func(T) error, wait <-chan struct{}, first bool, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if h.state == handleMoved || h.state == handleReleased || c.mode == modeReleased {
		if c.dropStarted && !c.dropFinished {
			return value, nil, c.dropWait, false, nil
		}
		return value, nil, nil, false, nil
	}
	if h.borrows != 0 {
		return value, nil, nil, false, c.conflict(OpRelease)
	}
	if c.mode != modeShared {
		return value, nil, nil, false, &MovedError{Operation: OpRelease}
	}
	if c.shares > 1 {
		h.state = handleReleased
		c.shares--
		return value, nil, nil, false, nil
	}
	if c.readers != 0 || c.writer {
		return value, nil, nil, false, c.conflict(OpRelease)
	}
	h.state = handleReleased
	c.shares = 0
	value = c.value
	var zero T
	c.value = zero
	c.mode = modeReleased
	c.dropStarted = true
	c.dropWait = make(chan struct{})
	return value, c.drop, nil, true, nil
}
