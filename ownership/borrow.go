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
	active   int
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

func (l *lease[T]) release() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released {
		return false
	}
	if l.active != 0 {
		return false
	}
	return l.cell.releaseLease(l)
}

// ReadBorrow is an explicitly released shared read borrow. It must not be
// copied after first use.
type ReadBorrow[T any] struct {
	_ noCopy

	lease   *lease[T]
	cleanup runtime.Cleanup
}

func newReadBorrow[T any](lease *lease[T]) *ReadBorrow[T] {
	borrow := &ReadBorrow[T]{lease: lease}
	borrow.cleanup = runtime.AddCleanup(borrow, cleanupLease[T], lease)
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
	b.lease.mu.Lock()
	active := b.lease.active
	b.lease.mu.Unlock()
	if active != 0 {
		return b.lease.cell.conflict(OpRelease)
	}
	if b.lease.release() {
		b.cleanup.Stop()
	}
	runtime.KeepAlive(b)
	return nil
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
	borrow.cleanup = runtime.AddCleanup(borrow, cleanupLease[T], lease)
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
	if err := b.lease.begin(borrowWrite, OpUpdate); err != nil {
		var zero R
		return zero, err
	}
	defer b.lease.end()

	result, err := fn(&b.lease.cell.value)
	runtime.KeepAlive(b)
	return result, err
}

// Release ends the borrow. It is idempotent.
func (b *WriteBorrow[T]) Release() error {
	if b == nil || b.lease == nil {
		return nil
	}
	b.lease.mu.Lock()
	active := b.lease.active
	b.lease.mu.Unlock()
	if active != 0 {
		return b.lease.cell.conflict(OpRelease)
	}
	if b.lease.release() {
		b.cleanup.Stop()
	}
	runtime.KeepAlive(b)
	return nil
}

// Close is an exact alias of Release.
func (b *WriteBorrow[T]) Close() error { return b.Release() }

func cleanupLease[T any](lease *lease[T]) {
	if lease.release() {
		logLeakedBorrow(lease.id, lease.kind)
	}
}
