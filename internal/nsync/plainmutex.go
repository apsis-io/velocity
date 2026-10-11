package nsync

import (
	"context"
	"time"
	"unsafe"
)

// Mutex is a zero-value-valid mutex in the shape of sync.Mutex: the state is
// inline, the zero value is unlocked and ready, and the fast paths are the
// same single compare-and-swaps TryMutex uses. The difference is the promise
// about copies. TryMutex holds its state behind a pointer installed at
// construction, so value copies share one lock; Mutex holds it inline, so
// the zero value works and value copies are independent locks.
//
// Choose Mutex for a mutex embedded by value in a struct that is never
// copied — a registry field, a guard inside a singleton. Choose TryMutex
// when value copies must share state, or when the zero value never needs to
// work. Neither type can be copied after first use, which is the standard
// mutex rule.
type Mutex struct {
	_ noCopy

	st lockState

	// The state is padded to its own cache line. A Mutex is typically
	// embedded beside the fields it guards, and a shared line would make
	// the lock word and those fields ping-pong across cores — the exact
	// false sharing an inline mutex invites and a pointer-based one avoids
	// by accident. Measured against the pointer form on an embedded use:
	// without the pad, parallel acquire ran ~5% behind; with it, at parity.
	_ [cacheLineSize - unsafe.Sizeof(lockState{})]byte
}

// Lock acquires the mutex, blocking if it is already locked.
func (m *Mutex) Lock() { m.st.lock() }

// TryLock acquires the mutex without waiting, reporting success.
func (m *Mutex) TryLock() bool { return m.st.tryLock() }

// LockContext acquires the mutex, blocking until it is available or ctx is
// done. It returns nil once the mutex is acquired, or ctx.Err() without
// acquiring it. If ctx is already done, LockContext does not try to acquire.
func (m *Mutex) LockContext(ctx context.Context) error { return m.st.lockContext(ctx) }

// TryLockTimeout tries immediately, then waits up to timeout for the mutex.
// A non-positive timeout is equivalent to TryLock.
func (m *Mutex) TryLockTimeout(timeout time.Duration) bool {
	return m.st.lockTimeout(timeout)
}

// Unlock releases the mutex. It panics if the mutex is unlocked.
func (m *Mutex) Unlock() { m.st.unlock() }
