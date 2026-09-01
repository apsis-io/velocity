package ownership

import (
	"runtime"
	"sync"
)

type borrowKind uint8

const (
	borrowRead borrowKind = iota
	borrowWrite
)

type lease[T any] struct {
	mu sync.Mutex

	cell     *cell[T]
	issuer   *handle
	id       uint64
	kind     borrowKind
	released bool
	active   int // Project/Update calls in flight through this lease
}

func (l *lease[T]) begin(kind borrowKind, op Operation) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released || l.kind != kind {
		return &ReleasedError{Operation: op}
	}
	l.active++
	return nil
}

func (l *lease[T]) end() {
	l.mu.Lock()
	l.active--
	l.mu.Unlock()
}

func (l *lease[T]) withWrite[R any](op Operation, fn func(*T) (R, error)) (R, error) {
	l.mu.Lock()
	if l.released || l.kind != borrowWrite {
		l.mu.Unlock()
		var zero R
		return zero, &ReleasedError{Operation: op}
	}
	if l.active != 0 {
		l.mu.Unlock()
		var zero R
		return zero, l.cell.conflict(op)
	}
	l.active++
	l.mu.Unlock()
	defer l.end()

	l.cell.mu.Lock()
	value := &l.cell.value
	l.cell.mu.Unlock()
	return fn(value)
}

func (l *lease[T]) release(op Operation) (released bool, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released {
		return false, nil
	}
	if l.active != 0 {
		return false, l.cell.conflict(op)
	}
	return l.cell.releaseLease(l), nil
}

// ReadBorrow is an explicitly released shared read borrow. It must not be
// copied after first use.
//
// Only the advanced borrow vocabulary allocates this wrapper, because only an
// advanced borrow can be leaked: scoped View/Mutate hold the lease directly
// and release it by defer. A leaked ReadBorrow blocks its cell until it is
// released; there is no runtime safety net. Under -tags=velocitydebug a
// runtime cleanup detects the leak once the handle is unreachable, logs it,
// and releases the lease so tests keep going. That is a diagnostic, not a
// guarantee, and production builds do not pay for it.
type ReadBorrow[T any] struct {
	_ noCopy

	lease   *lease[T]
	cleanup runtime.Cleanup
}

func newReadBorrow[T any](lease *lease[T]) *ReadBorrow[T] {
	borrow := &ReadBorrow[T]{lease: lease}
	borrow.cleanup = trackLeak(borrow, lease)
	return borrow
}

// Project invokes fn with a shallow copy of T while this borrow remains live.
func (b *ReadBorrow[T]) Project[R any](fn func(T) (R, error)) (R, error) {
	if fn == nil {
		var zero R
		return zero, &ProjectionError{Operation: OpProject}
	}
	if b == nil || b.lease == nil {
		var zero R
		return zero, &ReleasedError{Operation: OpProject}
	}
	if err := b.lease.begin(borrowRead, OpProject); err != nil {
		var zero R
		return zero, err
	}
	defer b.lease.end()

	b.lease.cell.mu.Lock()
	value := b.lease.cell.value
	b.lease.cell.mu.Unlock()
	result, err := fn(value)
	runtime.KeepAlive(b)
	return result, err
}

// Release ends the borrow. It is idempotent.
func (b *ReadBorrow[T]) Release() error {
	if b == nil || b.lease == nil {
		return nil
	}
	released, err := b.lease.release(OpRelease)
	if released {
		b.cleanup.Stop()
	}
	runtime.KeepAlive(b)
	return err
}

// Close is an exact alias of Release.
func (b *ReadBorrow[T]) Close() error { return b.Release() }

// WriteBorrow is an explicitly released exclusive mutable borrow. It must not
// be copied after first use.
type WriteBorrow[T any] struct {
	_ noCopy

	lease   *lease[T]
	cleanup runtime.Cleanup
}

func newWriteBorrow[T any](lease *lease[T]) *WriteBorrow[T] {
	borrow := &WriteBorrow[T]{lease: lease}
	borrow.cleanup = trackLeak(borrow, lease)
	return borrow
}

// Update invokes fn with exclusive mutable access while this borrow remains
// live. Mutations are not rolled back when fn returns an error.
func (b *WriteBorrow[T]) Update[R any](fn func(*T) (R, error)) (R, error) {
	if fn == nil {
		var zero R
		return zero, &ProjectionError{Operation: OpUpdate}
	}
	if b == nil || b.lease == nil {
		var zero R
		return zero, &ReleasedError{Operation: OpUpdate}
	}
	result, err := b.lease.withWrite(OpUpdate, fn)
	runtime.KeepAlive(b)
	return result, err
}

// Release ends the borrow. It is idempotent.
func (b *WriteBorrow[T]) Release() error {
	if b == nil || b.lease == nil {
		return nil
	}
	released, err := b.lease.release(OpRelease)
	if released {
		b.cleanup.Stop()
	}
	runtime.KeepAlive(b)
	return err
}

// Close is an exact alias of Release.
func (b *WriteBorrow[T]) Close() error { return b.Release() }
