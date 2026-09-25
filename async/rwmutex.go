package async

import (
	"context"
	"sync"
)

// RWMutex is a read/write lock whose RLock and Lock both wait under the
// caller's context — the thing sync.RWMutex cannot do, and the reason a
// read-mostly structure guarded by one cannot be drained at shutdown: a
// goroutine blocked on RLock is blocked on a runtime semaphore with no way to
// be told to stop.
//
//	read, err := mu.RLock(ctx)
//	if err != nil {
//	    return err
//	}
//	defer read.Release()
//
//	held, err := mu.Lock(ctx)
//	if err != nil {
//	    return err
//	}
//	defer held.Release()
//
// As with Mutex, the lock is released by Release on the returned Permit, so it
// is released exactly once and a forgotten one is reported by the lostrelease
// analyzer. The analyzer learns these from //velocity:acquires on the methods
// below, so a dropped read lock is a lint failure rather than a comment.
//
// **Writers take priority.** Once a writer is waiting, RLock blocks, so a
// stream of readers cannot starve a writer. This matches sync.RWMutex, and it
// is the reason a read lock is not simply "increment a counter": RLock has to
// be able to say no.
//
// **It is not reentrant, and it does not upgrade.** A goroutine holding the
// write lock cannot take a read lock without deadlocking against itself, and a
// goroutine holding a read lock cannot take the write lock until it has
// released every read lock it holds. A read lock is not a read lock plus a
// right to write. Both are true of sync.RWMutex; neither is caught here beyond
// the context bounding the wait.
//
// **Holding a read lock across a write is the deadlock this cannot save you
// from.** A caller that RLock, then calls something that Locks, holds a read
// lock for as long as the write lock waits for it. The context bounds the wait;
// it does not break the cycle.
type RWMutex struct {
	mu      sync.Mutex
	readers int  // read permits currently held
	waiting int  // writers blocked in Lock
	held    bool // a writer holds the lock
	waiters []chan struct{}
}

// NewRWMutex returns an unlocked RWMutex.
func NewRWMutex() *RWMutex { return &RWMutex{} }

// RLock takes a read lock, waiting under ctx. Any number of readers may hold
// the lock at once; a writer excludes them all, and once a writer is waiting
// RLock blocks so that readers cannot starve it.
//
//velocity:acquires
func (m *RWMutex) RLock(ctx context.Context) (*Permit, error) {
	if m == nil {
		return nil, &TaskError{Index: -1, Cause: ErrNilReceiver}
	}

	if ctx == nil {
		return nil, &TaskError{Index: -1, Cause: ErrNilContext}
	}

	for {
		m.mu.Lock()
		if !m.held && m.waiting == 0 {
			m.readers++
			m.mu.Unlock()

			return &Permit{rw: m}, nil
		}

		ch := m.register()
		m.mu.Unlock()

		select {
		case <-ctx.Done():
			m.mu.Lock()
			m.deregister(ch)
			m.mu.Unlock()

			return nil, context.Cause(ctx)
		case <-ch:
		}
	}
}

// Lock takes the write lock, waiting under ctx. It excludes every reader, and
// no other writer.
//
//velocity:acquires
func (m *RWMutex) Lock(ctx context.Context) (*Permit, error) {
	if m == nil {
		return nil, &TaskError{Index: -1, Cause: ErrNilReceiver}
	}

	if ctx == nil {
		return nil, &TaskError{Index: -1, Cause: ErrNilContext}
	}
	// Counted once, and decremented on the way out however the loop leaves —
	// otherwise a Lock that gave up would keep new readers blocked forever.
	counted := false

	for {
		m.mu.Lock()
		if !m.held && m.readers == 0 {
			m.held = true
			if counted {
				m.waiting--
			}
			m.mu.Unlock()

			return &Permit{rw: m, write: true}, nil
		}

		if !counted {
			m.waiting++
			counted = true
		}

		ch := m.register()
		m.mu.Unlock()

		select {
		case <-ctx.Done():
			m.mu.Lock()
			m.deregister(ch)

			if counted {
				m.waiting--
			}
			m.mu.Unlock()

			return nil, context.Cause(ctx)
		case <-ch:
		}
	}
}

// TryRLock takes a read lock if one can be taken without waiting: no writer
// holds the lock and none is waiting. It reports false, and holds nothing, if
// either is true.
//
//velocity:acquires
func (m *RWMutex) TryRLock() (*Permit, bool) {
	if m == nil {
		return nil, false
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.held || m.waiting != 0 {
		return nil, false
	}

	m.readers++

	return &Permit{rw: m}, true
}

// TryLock takes the write lock if it is free, without waiting.
//
//velocity:acquires
func (m *RWMutex) TryLock() (*Permit, bool) {
	if m == nil {
		return nil, false
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.held || m.readers != 0 {
		return nil, false
	}

	m.held = true

	return &Permit{rw: m, write: true}, true
}

// unlockRead releases one read permit. Writers are the only waiters a reader
// release can unblock, and only once the last reader is gone — so the wake is
// deliberately not unconditional. Read locks are the common case and a
// broadcast per release would be an allocation on the uncontended path.
func (m *RWMutex) unlockRead() {
	m.mu.Lock()

	m.readers--
	checkReadRelease(m.readers)

	if m.readers == 0 {
		m.wake()
	}
	m.mu.Unlock()
}

func (m *RWMutex) unlockWrite() {
	m.mu.Lock()
	checkWriteRelease(m.held)
	m.held = false
	m.wake()
	m.mu.Unlock()
}

// register adds a waiter and returns its channel. Called with m.mu held.
func (m *RWMutex) register() chan struct{} {
	ch := make(chan struct{})
	m.waiters = append(m.waiters, ch)

	return ch
}

// deregister removes a waiter that gave up. A channel left behind would be
// closed harmlessly by the next wake, but the slice would grow for as long as
// the lock stayed contended. Called with m.mu held.
func (m *RWMutex) deregister(ch chan struct{}) {
	for i, w := range m.waiters {
		if w == ch {
			m.waiters = append(m.waiters[:i], m.waiters[i+1:]...)
			return
		}
	}
}

// wake closes every registered waiter's channel and forgets them. Closing
// rather than signalling one is what makes it a broadcast, and a broadcast is
// what a state change means: a waiter cannot know whether the state it wanted
// is the one that just arrived. Called with m.mu held.
func (m *RWMutex) wake() {
	for _, ch := range m.waiters {
		close(ch)
	}

	m.waiters = nil
}
