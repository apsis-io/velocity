package ownership

import "runtime"

// NewFrozen creates a read-only handle directly, for values that are published
// rather than mutated: configuration, manifests, policy snapshots. It saves
// constructing an Owner only to Freeze it immediately.
//
// Frozen is shallow. A frozen []byte, map, pointer, or interface still exposes
// whatever interior storage it points at; freezing the handle does not deep-copy
// or seal what the value refers to. Supply a real Clone and hand out Snapshot
// results, or use an opaque value type, when interior mutation matters.
func NewFrozen[T any](value T, opts ...Option[T]) (*Frozen[T], error) {
	if len(opts) == 0 {
		return &Frozen[T]{c: &cell[T]{value: value, mode: modeFrozen, shares: 1}}, nil
	}
	cfg, err := buildConfig(opts)
	if err != nil {
		return nil, err
	}
	return &Frozen[T]{c: &cell[T]{value: value, mode: modeFrozen, shares: 1, drop: cfg.drop, clone: cfg.clone}}, nil
}

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

// View runs fn against the value under a read borrow that lasts exactly as
// long as the call. See Owner.View, including the rule that the value must
// not outlive the call.
func (f *Frozen[T]) View[R any](fn func(T) (R, error)) (R, error) {
	if f == nil {
		var zero R
		return zero, &ReleasedError{Operation: OpBorrow}
	}
	return scopedView(f.c, &f.h, modeFrozen, fn)
}

// WithRead is View for callbacks that report only an error.
func (f *Frozen[T]) WithRead(fn func(T) error) error {
	_, err := f.View(errOnly(fn))
	return err
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
	return f.View(clone)
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
