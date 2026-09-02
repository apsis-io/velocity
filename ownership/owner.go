package ownership

import (
	"io"
	"runtime"
)

// NewCloser owns an io.Closer, releasing it by calling Close. It is the common
// resource case, which otherwise requires writing the Drop by hand:
//
//	conn := ownership.NewCloser(rawConn)
//	defer conn.Close()
//
// It cannot fail, so it returns no error. Use New with WithDrop when cleanup is
// not exactly Close, or WithClone alongside it.
func NewCloser[T io.Closer](value T) *Owner[T] {
	return &Owner[T]{c: &cell[T]{
		value: value,
		mode:  modeUnique,
		drop:  func(closer T) error { return closer.Close() },
	}}
}

// Owner holds the unique ownership handle for a value. It must not be copied
// after first use.
type Owner[T any] struct {
	_ noCopy
	c *cell[T]
	h handle
}

// Own creates a unique owner with no Drop or Clone. It cannot fail, so it
// returns no error; it is New for the case where there is nothing to
// configure, which is most of them.
func Own[T any](value T) *Owner[T] {
	return &Owner[T]{c: &cell[T]{value: value, mode: modeUnique}}
}

// New creates a unique owner with options. Without options it is exactly
// Own, which does not make the caller handle an error that cannot happen.
func New[T any](value T, opts ...Option[T]) (*Owner[T], error) {
	if len(opts) == 0 {
		return Own(value), nil
	}
	cfg, err := buildConfig(opts)
	if err != nil {
		return nil, err
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
//
//velocity:acquires
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
//
//velocity:acquires
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

// View runs fn against the value under a read borrow that lasts exactly as
// long as the call. Concurrent Views coexist; a Mutate meanwhile reports
// ErrConflict.
//
// fn receives a shallow copy that must not outlive the call. Retaining a
// slice, map, pointer, or interface reached through it escapes the borrow,
// and nothing can revoke it afterwards.
func (o *Owner[T]) View[R any](fn func(T) (R, error)) (R, error) {
	if o == nil {
		var zero R
		return zero, &ReleasedError{Operation: OpBorrow}
	}
	return scopedView(o.c, &o.h, modeUnique, fn)
}

// Mutate runs fn with exclusive mutable access under a write borrow that
// lasts exactly as long as the call. Mutations are not rolled back when fn
// returns an error.
func (o *Owner[T]) Mutate[R any](fn func(*T) (R, error)) (R, error) {
	if o == nil {
		var zero R
		return zero, &ReleasedError{Operation: OpBorrowMut}
	}
	return scopedMutate(o.c, &o.h, modeUnique, fn)
}

// WithRead is View for callbacks that report only an error.
func (o *Owner[T]) WithRead(fn func(T) error) error {
	_, err := o.View(errOnly(fn))
	return err
}

// WithWrite is Mutate for callbacks that report only an error.
func (o *Owner[T]) WithWrite(fn func(*T) error) error {
	_, err := o.Mutate(errOnly(fn))
	return err
}

// Snapshot clones the value under a temporary read borrow.
func (o *Owner[T]) Snapshot() (T, error) {
	if o == nil || o.c == nil {
		var zero T
		return zero, &ReleasedError{Operation: OpSnapshot}
	}
	o.c.mu.Lock()
	if err := o.c.checkHandle(&o.h, OpSnapshot); err != nil {
		o.c.mu.Unlock()
		var zero T
		return zero, err
	}
	clone := o.c.clone
	o.c.mu.Unlock()
	if clone == nil {
		var zero T
		return zero, &NoCloneError{Operation: OpSnapshot}
	}
	return o.View(clone)
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
		return nil, c.conflictLocked(OpMove)
	}
	o.h.state = handleMoved
	return &Owner[T]{c: c}, nil
}

// Detach consumes this Owner and returns the bare value, transferring cleanup
// responsibility to the caller: Drop does not run, now or ever.
//
// Use it when the caller genuinely takes over the resource. Do not use it
// merely to pass a value through an API that wants a bare T, because nothing
// will close what the Owner was going to close. Release is the operation that
// keeps cleanup with the Owner.
func (o *Owner[T]) Detach() (T, error) {
	if o == nil || o.c == nil {
		var zero T
		return zero, &ReleasedError{Operation: OpDetach}
	}
	c := o.c
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.checkHandle(&o.h, OpDetach); err != nil {
		var zero T
		return zero, err
	}
	if c.mode != modeUnique || o.h.borrows != 0 || c.readers != 0 || c.writer {
		var zero T
		return zero, c.conflictLocked(OpDetach)
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
		return nil, c.conflictLocked(OpIntoShared)
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
	value, drop, first, err := c.beginOwnerRelease(&o.h)
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
	runtime.KeepAlive(o)
	return dropErr
}

// Close is an exact alias of Release.
func (o *Owner[T]) Close() error { return o.Release() }

func (c *cell[T]) beginOwnerRelease(h *handle) (value T, drop func(T) error, first bool, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if h.state == handleMoved || h.state == handleReleased || c.mode == modeReleased {
		return value, nil, false, nil
	}
	if h.borrows != 0 || c.readers != 0 || c.writer {
		return value, nil, false, c.conflictLocked(OpRelease)
	}
	h.state = handleReleased
	value = c.value
	var zero T
	c.value = zero
	c.mode = modeReleased
	return value, c.drop, true, nil
}

func (c *cell[T]) finishDrop(err error) {
	c.mu.Lock()
	c.dropErr = err
	c.mu.Unlock()
}
