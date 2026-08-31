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

// New creates a unique owner.
func New[T any](value T, opts ...Option[T]) (*Owner[T], error) {
	if len(opts) == 0 {
		return &Owner[T]{c: &cell[T]{value: value, mode: modeUnique}}, nil
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

// BorrowUntracked is Borrow without the runtime cleanup that reclaims a
// leaked borrow. It allocates less, but a caller that never releases the
// returned handle blocks this cell permanently rather than having the borrow
// reclaimed once the handle becomes unreachable.
//
// Prefer the scoped Read, which cannot leak. Prefer Borrow whenever release
// is not obviously guaranteed on every path, including panics.
func (o *Owner[T]) BorrowUntracked() (*ReadBorrow[T], error) {
	if o == nil || o.c == nil {
		return nil, &ReleasedError{Operation: OpBorrow}
	}
	lease, err := o.c.acquireRead(&o.h, modeUnique)
	if err != nil {
		return nil, err
	}
	return newUntrackedReadBorrow(lease), nil
}

// BorrowMutUntracked is BorrowMut with the same trade BorrowUntracked makes.
func (o *Owner[T]) BorrowMutUntracked() (*WriteBorrow[T], error) {
	if o == nil || o.c == nil {
		return nil, &ReleasedError{Operation: OpBorrowMut}
	}
	lease, err := o.c.acquireWrite(&o.h, modeUnique)
	if err != nil {
		return nil, err
	}
	return newUntrackedWriteBorrow(lease), nil
}

// Read runs fn under a callback-scoped shared read borrow.
func (o *Owner[T]) Read[R any](fn func(ReadAccess[T]) (R, error)) (R, error) {
	if fn == nil {
		var zero R
		return zero, &ProjectionError{Operation: OpProject}
	}
	if o == nil || o.c == nil {
		var zero R
		return zero, &ReleasedError{Operation: OpBorrow}
	}
	lease, err := o.c.acquireRead(&o.h, modeUnique)
	if err != nil {
		var zero R
		return zero, err
	}
	defer lease.closeScoped()
	return fn(ReadAccess[T]{lease: lease})
}

// Write runs fn under a callback-scoped exclusive mutable borrow.
func (o *Owner[T]) Write[R any](fn func(WriteAccess[T]) (R, error)) (R, error) {
	if fn == nil {
		var zero R
		return zero, &ProjectionError{Operation: OpUpdate}
	}
	if o == nil || o.c == nil {
		var zero R
		return zero, &ReleasedError{Operation: OpBorrowMut}
	}
	lease, err := o.c.acquireWrite(&o.h, modeUnique)
	if err != nil {
		var zero R
		return zero, err
	}
	defer lease.closeScoped()
	return fn(WriteAccess[T]{lease: lease})
}

// View runs fn against the value under a callback-scoped read borrow. It is
// Read without the intermediate ReadAccess, for the common case of projecting
// a value out.
//
// The same lifetime rule applies: fn receives a shallow copy that must not
// outlive the call. Retaining a slice, map, pointer, or interface reached
// through it escapes the borrow.
func (o *Owner[T]) View[R any](fn func(T) (R, error)) (R, error) {
	if fn == nil {
		var zero R
		return zero, &ProjectionError{Operation: OpProject}
	}
	return o.Read(func(access ReadAccess[T]) (R, error) { return access.Project(fn) })
}

// Mutate runs fn against the value under a callback-scoped exclusive borrow.
// It is Write without the intermediate WriteAccess. Mutations are not rolled
// back when fn returns an error.
func (o *Owner[T]) Mutate[R any](fn func(*T) (R, error)) (R, error) {
	if fn == nil {
		var zero R
		return zero, &ProjectionError{Operation: OpUpdate}
	}
	return o.Write(func(access WriteAccess[T]) (R, error) { return access.Update(fn) })
}

// WithRead is View for callbacks that report only an error.
func (o *Owner[T]) WithRead(fn func(T) error) error {
	if fn == nil {
		return &ProjectionError{Operation: OpProject}
	}
	_, err := o.View(func(value T) (struct{}, error) { return struct{}{}, fn(value) })
	return err
}

// WithWrite is Mutate for callbacks that report only an error.
func (o *Owner[T]) WithWrite(fn func(*T) error) error {
	if fn == nil {
		return &ProjectionError{Operation: OpUpdate}
	}
	_, err := o.Mutate(func(value *T) (struct{}, error) { return struct{}{}, fn(value) })
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
//
// Detach is an exact alias of IntoValue, named for what it does to ownership
// rather than for the value it returns.
func (o *Owner[T]) Detach() (T, error) { return o.IntoValue() }

// IntoValue consumes this Owner and returns the bare value without running Drop.
// See Detach, which is the same operation named after its effect on cleanup.
func (o *Owner[T]) IntoValue() (T, error) {
	if o == nil || o.c == nil {
		var zero T
		return zero, &ReleasedError{Operation: OpIntoValue}
	}
	c := o.c
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.checkHandle(&o.h, OpIntoValue); err != nil {
		var zero T
		return zero, err
	}
	if c.mode != modeUnique || o.h.borrows != 0 || c.readers != 0 || c.writer {
		var zero T
		return zero, c.conflictLocked(OpIntoValue)
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
