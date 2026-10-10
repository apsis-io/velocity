package async

import (
	"context"
	"sync"

	"github.com/vburenin/nsync"
)

// Semaphore is a counting semaphore whose Acquire waits under the caller's
// context. It is x/sync/semaphore for the common case of unit weights, with
// the permit as a value that is released exactly once and can be found by
// the lostrelease analyzer when it is not.
//
//	permit, err := sem.Acquire(ctx)
//	if err != nil {
//	    return err
//	}
//	defer permit.Release()
type Semaphore struct {
	permits *nsync.Semaphore
}

// NewSemaphore returns a semaphore admitting n holders at once.
func NewSemaphore(n int) (*Semaphore, error) {
	if n <= 0 {
		return nil, &TaskError{Index: -1, Cause: ErrInvalidLimit}
	}

	return &Semaphore{permits: nsync.NewSemaphore(n)}, nil
}

// Acquire takes a permit, waiting under ctx for one to be released. A
// context that is already done fails at once even if a permit is free, so
// a cancelled caller never proceeds by luck.
//
//velocity:acquires
func (s *Semaphore) Acquire(ctx context.Context) (*Permit, error) {
	if s == nil {
		return nil, &TaskError{Index: -1, Cause: ErrNilReceiver}
	}

	if err := ctx.Err(); err != nil {
		return nil, context.Cause(ctx)
	}

	// AcquireContext checks ctx before trying, matching the contract above:
	// a cancelled caller never proceeds by luck. The pool reports the
	// context's cause, as the channel select did.
	if err := s.permits.AcquireContext(ctx); err != nil {
		return nil, context.Cause(ctx)
	}

	return &Permit{permits: s.permits}, nil
}

// TryAcquire takes a permit if one is free, without waiting.
//
//velocity:acquires
func (s *Semaphore) TryAcquire() (*Permit, bool) {
	if s == nil {
		return nil, false
	}

	if !s.permits.TryAcquire() {
		return nil, false
	}

	return &Permit{permits: s.permits}, true
}

// Permit is one held unit of a Semaphore, the lock of a Mutex, or a read or
// write lock of an RWMutex. Release hands it back exactly once; later calls do
// nothing.
type Permit struct {
	permits *nsync.Semaphore
	once    sync.Once
	// rw is set only by RWMutex, whose read and write locks are not tokens in a
	// channel. It is nil on the Semaphore path, which is the hot one, and
	// Release's nil check is the whole cost of sharing the type.
	//
	// This started as a func() hook, which cost a second allocation per lock
	// acquire because the closure escapes. Two fields are a larger struct and
	// one allocation rather than a smaller struct and two.
	rw    *RWMutex
	write bool
}

// Release hands the permit back. It is idempotent, so a deferred Release
// beside an explicit one is safe.
func (p *Permit) Release() {
	if p == nil {
		return
	}

	p.once.Do(func() {
		if p.rw != nil {
			if p.write {
				p.rw.unlockWrite()
			} else {
				p.rw.unlockRead()
			}

			return
		}

		p.permits.Release()
	})
}

// Mutex is an exclusive lock whose Lock waits under the caller's context —
// the thing sync.Mutex cannot do and x/sync/semaphore.NewWeighted(1) is
// usually standing in for. Unlock is Release on the returned Permit, so a
// lock is released exactly once and a forgotten one is reported by the
// lostrelease analyzer.
//
//	held, err := mu.Lock(ctx)
//	if err != nil {
//	    return err
//	}
//	defer held.Release()
//
// The Permit costs one allocation on every acquire, which x/sync's
// semaphore.NewWeighted(1) does not pay uncontended — but x/sync allocates
// a waiter per blocked acquire, which this does not. The interleaved
// measurement against x/sync in the record's history described the
// channel-permit version; the internals have since moved, so treat any
// inherited number as stale and measure the site you care about.
//
// Choose on the call site, not on those numbers. End to end at the sites
// this replaces, the difference was below run-to-run variance: re-running
// the unchanged baseline moved more than the change did, and the sign of
// the delta flipped depending on which baseline it was compared against.
// The lock is a few percent of the work it guards, so measure your own site
// or pick on the API.
type Mutex struct {
	sem Semaphore
}

// NewMutex returns an unlocked Mutex.
func NewMutex() *Mutex {
	return &Mutex{sem: Semaphore{permits: nsync.NewSemaphore(1)}}
}

// Lock takes the lock, waiting under ctx.
//
//velocity:acquires
func (m *Mutex) Lock(ctx context.Context) (*Permit, error) {
	if m == nil {
		return nil, &TaskError{Index: -1, Cause: ErrNilReceiver}
	}

	return m.sem.Acquire(ctx)
}

// TryLock takes the lock if it is free, without waiting.
//
//velocity:acquires
func (m *Mutex) TryLock() (*Permit, bool) {
	if m == nil {
		return nil, false
	}

	return m.sem.TryAcquire()
}
