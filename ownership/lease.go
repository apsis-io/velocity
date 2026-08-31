package ownership

import "sync"

// Lease owns a resource that is held and then handed back, where the resource
// is identified by a value rather than represented by one: a network permit, an
// IP-pool allocation, a UID-base assignment, a systemd unit reference.
//
// It is deliberately not borrow-checked. Those values are small, copyable
// identifiers, and excluding concurrent readers from an IP address protects
// nothing; the property that matters is release-exactly-once, and no use after
// it. Value therefore hands back a copy directly, after checking the lease is
// still held, rather than routing through a callback.
//
// Use an Owner instead when T is a resource whose interior must not be aliased
// or mutated concurrently, and where borrow enforcement is the point.
//
// A Lease is safe for concurrent use.
type Lease[T any] struct {
	_ noCopy

	mu       sync.Mutex
	value    T
	release  func(T) error
	released bool
	relErr   error
}

// NewLease holds value until Release hands it back through release. The release
// callback must return normally and should be bounded: it is called while other
// goroutines may be blocked in Value, and is not passed a context. Add
// cancellation only if a specific resource genuinely needs it, and only where
// the semantics are defined.
func NewLease[T any](value T, release func(T) error) (*Lease[T], error) {
	if release == nil {
		return nil, &ConfigError{Option: "lease release", Reason: ErrNilOption}
	}
	return &Lease[T]{value: value, release: release}, nil
}

// Value returns a copy of the leased value while the lease is still held, and
// ErrReleased once it is not. That catches use-after-release, which is the
// mistake this type exists to prevent.
//
// It gives no aliasing protection: if T carries a pointer, slice, or map, the
// copy still refers to the same storage. Use an Owner when that matters.
func (l *Lease[T]) Value() (T, error) {
	if l == nil {
		var zero T
		return zero, &ReleasedError{Operation: OpProject}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released {
		var zero T
		return zero, &ReleasedError{Operation: OpProject}
	}
	return l.value, nil
}

// Held reports whether the lease still holds its resource.
func (l *Lease[T]) Held() bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return !l.released
}

// Move transfers the lease to a fresh handle and spends this one, for handing a
// resource to another goroutine or struct without leaving a second handle that
// could release it. The original reports ErrReleased afterwards.
func (l *Lease[T]) Move() (*Lease[T], error) {
	if l == nil {
		return nil, &ReleasedError{Operation: OpMove}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released {
		return nil, &ReleasedError{Operation: OpMove}
	}
	l.released = true
	return &Lease[T]{value: l.value, release: l.release}, nil
}

// Release hands the resource back, at most once. Later calls return the first
// call's error without invoking release again, so a deferred Release stays
// correct alongside an explicit one.
func (l *Lease[T]) Release() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	if l.released {
		err := l.relErr
		l.mu.Unlock()
		return err
	}
	l.released = true
	value, release := l.value, l.release
	var zero T
	l.value = zero
	l.mu.Unlock()

	err := release(value)

	l.mu.Lock()
	l.relErr = err
	l.mu.Unlock()
	return err
}

// Close is an exact alias of Release.
func (l *Lease[T]) Close() error { return l.Release() }
