package ownership

import (
	"fmt"
	"runtime"
)

// Owner holds the unique ownership handle for a value. It must not be copied
// after first use.
type Owner[T any] struct {
	_ noCopy
	c *cell[T]
	h handle
}

// New creates a unique owner.
func New[T any](value T, opts ...Option[T]) (*Owner[T], error) {
	cfg := config[T]{}
	for i, opt := range opts {
		if opt == nil {
			return nil, &ConfigError{Option: fmt.Sprintf("option %d", i), Reason: ErrProjection}
		}
		if err := opt.apply(&cfg); err != nil {
			return nil, err
		}
	}
	return &Owner[T]{c: &cell[T]{value: value, mode: modeUnique, drop: cfg.drop, clone: cfg.clone}}, nil
}

// State returns a synchronized ownership snapshot.
func (o *Owner[T]) State() State {
	if o == nil {
		return State{Released: true}
	}
	return o.c.stateFor(&o.h)
}

// Borrow acquires an advanced shared read borrow.
func (o *Owner[T]) Borrow() (*ReadBorrow[T], error) {
	if o == nil || o.c == nil {
		return nil, &ReleasedError{Operation: OpBorrow}
	}
	lease, err := o.c.acquireRead(&o.h, modeUnique)
	if err != nil {
		return nil, err
	}
	return newReadBorrow(lease), nil
}

// BorrowMut acquires an advanced exclusive mutable borrow.
func (o *Owner[T]) BorrowMut() (*WriteBorrow[T], error) {
	if o == nil || o.c == nil {
		return nil, &ReleasedError{Operation: OpBorrowMut}
	}
	lease, err := o.c.acquireWrite(&o.h, modeUnique)
	if err != nil {
		return nil, err
	}
	return newWriteBorrow(lease), nil
}

// Read runs fn under a callback-scoped shared read borrow.
func (o *Owner[T]) Read[R any](fn func(ReadAccess[T]) (R, error)) (R, error) {
	if fn == nil {
		var zero R
		return zero, &ProjectionError{Operation: OpProject}
	}
	borrow, err := o.Borrow()
	if err != nil {
		var zero R
		return zero, err
	}
	defer borrow.Release()
	result, err := borrow.Project(func(value T) (R, error) { return fn(ReadAccess[T]{lease: borrow.lease}) })
	return result, err
}

// Write runs fn under a callback-scoped exclusive mutable borrow.
func (o *Owner[T]) Write[R any](fn func(WriteAccess[T]) (R, error)) (R, error) {
	if fn == nil {
		var zero R
		return zero, &ProjectionError{Operation: OpUpdate}
	}
	borrow, err := o.BorrowMut()
	if err != nil {
		var zero R
		return zero, err
	}
	defer borrow.Release()
	return fn(WriteAccess[T]{lease: borrow.lease})
}

// Snapshot clones the value under a temporary read borrow.
func (o *Owner[T]) Snapshot() (T, error) {
	if o == nil || o.c == nil {
		var zero T
		return zero, &ReleasedError{Operation: OpSnapshot}
	}
	o.c.mu.Lock()
	clone := o.c.clone
	o.c.mu.Unlock()
	if clone == nil {
		var zero T
		return zero, &NoCloneError{Operation: OpSnapshot}
	}
	return o.Read(func(access ReadAccess[T]) (T, error) {
		return access.Project(clone)
	})
}

// Move transfers the cell to a fresh Owner and invalidates this handle.
func (o *Owner[T]) Move() (*Owner[T], error) {
	if o == nil || o.c == nil {
		return nil, &ReleasedError{Operation: OpMove}
	}
	c := o.c
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.checkHandle(&o.h, OpMove); err != nil {
		return nil, err
	}
	if c.mode != modeUnique {
		return nil, &MovedError{Operation: OpMove}
	}
	if o.h.borrows != 0 || c.readers != 0 || c.writer {
		return nil, c.conflict(OpMove)
	}
	o.h.state = handleMoved
	return &Owner[T]{c: c}, nil
}

// Take consumes this Owner and returns the bare value without running Drop.
func (o *Owner[T]) Take() (T, error) {
	if o == nil || o.c == nil {
		var zero T
		return zero, &ReleasedError{Operation: OpTake}
	}
	c := o.c
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.checkHandle(&o.h, OpTake); err != nil {
		var zero T
		return zero, err
	}
	if c.mode != modeUnique || o.h.borrows != 0 || c.readers != 0 || c.writer {
		var zero T
		return zero, c.conflict(OpTake)
	}
	value := c.value
	var zero T
	c.value = zero
	c.mode = modeReleased
	o.h.state = handleMoved
	return value, nil
}

// IntoShared consumes this Owner and creates the first Shared handle.
func (o *Owner[T]) IntoShared() (*Shared[T], error) {
	if o == nil || o.c == nil {
		return nil, &ReleasedError{Operation: OpIntoShared}
	}
	c := o.c
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.checkHandle(&o.h, OpIntoShared); err != nil {
		return nil, err
	}
	if c.mode != modeUnique || o.h.borrows != 0 || c.readers != 0 || c.writer {
		return nil, c.conflict(OpIntoShared)
	}
	o.h.state = handleMoved
	c.mode = modeShared
	c.shares = 1
	return &Shared[T]{c: c}, nil
}

// Release gives up unique ownership and invokes Drop at most once.
func (o *Owner[T]) Release() error {
	if o == nil || o.c == nil {
		return nil
	}
	c := o.c
	value, drop, wait, first, err := c.beginOwnerRelease(&o.h)
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
	runtime.KeepAlive(o)
	return dropErr
}

// Close is an exact alias of Release.
func (o *Owner[T]) Close() error { return o.Release() }

func (c *cell[T]) beginOwnerRelease(h *handle) (value T, drop func(T) error, wait <-chan struct{}, first bool, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if h.state == handleMoved || h.state == handleReleased || c.mode == modeReleased {
		if c.dropStarted && !c.dropFinished {
			return value, nil, c.dropWait, false, nil
		}
		return value, nil, nil, false, nil
	}
	if h.borrows != 0 || c.readers != 0 || c.writer {
		return value, nil, nil, false, c.conflict(OpRelease)
	}
	h.state = handleReleased
	value = c.value
	var zero T
	c.value = zero
	c.mode = modeReleased
	c.dropStarted = true
	c.dropWait = make(chan struct{})
	return value, c.drop, nil, true, nil
}

func (c *cell[T]) finishDrop(err error) {
	c.mu.Lock()
	c.dropErr = err
	c.dropFinished = true
	close(c.dropWait)
	c.mu.Unlock()
}
