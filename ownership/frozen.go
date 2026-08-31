package ownership

import "runtime"

// Frozen is one explicitly counted handle to a value that can no longer be
// mutated. Unlike Shared, which rejects a conflicting write at runtime, Frozen
// exposes no write operation at all: there is no Write, BorrowMut, or Update to
// call, so "reads only" is a property of the type rather than a convention.
//
// It must not be copied; use Clone to create another handle. Thaw with
// IntoOwner once this is the sole unborrowed handle.
type Frozen[T any] struct {
	_ noCopy
	c *cell[T]
	h handle
}

// Freeze consumes this Owner and returns the first Frozen handle. It requires
// the same exclusivity as IntoShared: unique ownership with no outstanding
// borrows.
func (o *Owner[T]) Freeze() (*Frozen[T], error) {
	if o == nil || o.c == nil {
		return nil, &ReleasedError{Operation: OpFreeze}
	}
	c := o.c
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.checkHandle(&o.h, OpFreeze); err != nil {
		return nil, err
	}
	if c.mode != modeUnique || o.h.borrows != 0 || c.readers != 0 || c.writer {
		return nil, c.conflictLocked(OpFreeze)
	}
	o.h.state = handleMoved
	c.mode = modeFrozen
	c.shares = 1
	return &Frozen[T]{c: c}, nil
}

// State returns a synchronized ownership snapshot.
func (f *Frozen[T]) State() State {
	if f == nil {
		return State{Released: true}
	}
	return f.c.stateFor(&f.h)
}

// Clone creates another explicitly counted Frozen handle.
func (f *Frozen[T]) Clone() (*Frozen[T], error) {
	if f == nil || f.c == nil {
		return nil, &ReleasedError{Operation: OpClone}
	}
	c := f.c
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.checkHandle(&f.h, OpClone); err != nil {
		return nil, err
	}
	if c.mode != modeFrozen {
		return nil, &MovedError{Operation: OpClone}
	}
	c.shares++
	return &Frozen[T]{c: c}, nil
}

// Borrow acquires an advanced shared read borrow.
func (f *Frozen[T]) Borrow() (*ReadBorrow[T], error) {
	if f == nil || f.c == nil {
		return nil, &ReleasedError{Operation: OpBorrow}
	}
	lease, err := f.c.acquireRead(&f.h, modeFrozen)
	if err != nil {
		return nil, err
	}
	return newReadBorrow(lease), nil
}

// BorrowUntracked is Borrow without the runtime cleanup that reclaims a leaked
// borrow. See Owner.BorrowUntracked for the trade it makes.
func (f *Frozen[T]) BorrowUntracked() (*ReadBorrow[T], error) {
	if f == nil || f.c == nil {
		return nil, &ReleasedError{Operation: OpBorrow}
	}
	lease, err := f.c.acquireRead(&f.h, modeFrozen)
	if err != nil {
		return nil, err
	}
	return newUntrackedReadBorrow(lease), nil
}

// Read runs fn under a callback-scoped shared read borrow.
func (f *Frozen[T]) Read[R any](fn func(ReadAccess[T]) (R, error)) (R, error) {
	if fn == nil {
		var zero R
		return zero, &ProjectionError{Operation: OpProject}
	}
	if f == nil || f.c == nil {
		var zero R
		return zero, &ReleasedError{Operation: OpBorrow}
	}
	lease, err := f.c.acquireRead(&f.h, modeFrozen)
	if err != nil {
		var zero R
		return zero, err
	}
	defer lease.closeScoped()
	return fn(ReadAccess[T]{lease: lease})
}

// Snapshot clones the value under a temporary read borrow.
func (f *Frozen[T]) Snapshot() (T, error) {
	if f == nil || f.c == nil {
		var zero T
		return zero, &ReleasedError{Operation: OpSnapshot}
	}
	f.c.mu.Lock()
	if err := f.c.checkHandle(&f.h, OpSnapshot); err != nil {
		f.c.mu.Unlock()
		var zero T
		return zero, err
	}
	clone := f.c.clone
	f.c.mu.Unlock()
	if clone == nil {
		var zero T
		return zero, &NoCloneError{Operation: OpSnapshot}
	}
	return f.Read(func(access ReadAccess[T]) (T, error) {
		return access.Project(clone)
	})
}

// IntoOwner thaws the sole unborrowed Frozen handle back into a unique Owner,
// restoring the ability to mutate. On conflict, it leaves this handle
// unchanged.
func (f *Frozen[T]) IntoOwner() (*Owner[T], error) {
	if f == nil || f.c == nil {
		return nil, &ReleasedError{Operation: OpIntoOwner}
	}
	c := f.c
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.checkHandle(&f.h, OpIntoOwner); err != nil {
		return nil, err
	}
	if c.mode != modeFrozen || c.shares != 1 || f.h.borrows != 0 || c.readers != 0 || c.writer {
		return nil, c.conflictLocked(OpIntoOwner)
	}
	f.h.state = handleMoved
	c.shares = 0
	c.mode = modeUnique
	return &Owner[T]{c: c}, nil
}

// Release gives up this Frozen handle. The final release invokes Drop at most
// once. A handle with one of its own outstanding borrows cannot be released.
func (f *Frozen[T]) Release() error {
	if f == nil || f.c == nil {
		return nil
	}
	c := f.c
	value, drop, first, err := c.beginCountedRelease(&f.h, modeFrozen)
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
	runtime.KeepAlive(f)
	return dropErr
}

// Close is an exact alias of Release.
func (f *Frozen[T]) Close() error { return f.Release() }
