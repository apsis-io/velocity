package ownership

// closedDrain answers Drained for a handle with nothing left to wait for.
var closedDrain = func() <-chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}()

// Seal refuses further borrows so a retirement in progress cannot be extended,
// and is the half of graceful shutdown a caller cannot implement alone: only
// the cell knows the borrow count, and only it can turn a new Borrow away.
//
// Waiting is left to the caller, which is why this package still never blocks.
// Pair it with Drained:
//
//	owner.Seal()
//	select {
//	case <-owner.Drained():
//	case <-ctx.Done():
//	    return ctx.Err()
//	}
//	return owner.Release()
//
// Existing borrows are untouched and must still be released. Sealing is
// irreversible and idempotent, and applies to the value rather than to one
// handle: every handle to a sealed value refuses new borrows. It does not
// release anything by itself.
func (o *Owner[T]) Seal() error {
	if o == nil || o.c == nil {
		return &ReleasedError{Operation: OpSeal}
	}
	return o.c.seal(&o.h)
}

// Drained closes once the value is sealed and its last borrow has gone, which
// is the point Release is guaranteed not to report a conflict.
//
// It only ever closes after Seal, because without sealing a borrow count of
// zero is transient and a closed channel is not. A released or spent handle
// returns an already-closed channel, since it has nothing to wait for.
func (o *Owner[T]) Drained() <-chan struct{} {
	if o == nil || o.c == nil {
		return closedDrain
	}
	return o.c.drainedChan()
}

// Seal refuses further borrows of the shared value. See Owner.Seal; sealing
// applies to the value, so every Shared handle to it is affected.
func (s *Shared[T]) Seal() error {
	if s == nil || s.c == nil {
		return &ReleasedError{Operation: OpSeal}
	}
	return s.c.seal(&s.h)
}

// Drained closes once the value is sealed and its last borrow has gone. See
// Owner.Drained.
func (s *Shared[T]) Drained() <-chan struct{} {
	if s == nil || s.c == nil {
		return closedDrain
	}
	return s.c.drainedChan()
}

// Seal refuses further borrows of the frozen value. See Owner.Seal.
func (f *Frozen[T]) Seal() error {
	if f == nil || f.c == nil {
		return &ReleasedError{Operation: OpSeal}
	}
	return f.c.seal(&f.h)
}

// Drained closes once the value is sealed and its last borrow has gone. See
// Owner.Drained.
func (f *Frozen[T]) Drained() <-chan struct{} {
	if f == nil || f.c == nil {
		return closedDrain
	}
	return f.c.drainedChan()
}
