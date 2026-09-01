package async

import (
	"context"
	"sync"
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
	permits chan struct{}
}

// NewSemaphore returns a semaphore admitting n holders at once.
func NewSemaphore(n int) (*Semaphore, error) {
	if n <= 0 {
		return nil, &PlanError{Index: -1, Cause: ErrInvalidLimit}
	}
	return &Semaphore{permits: make(chan struct{}, n)}, nil
}

// Acquire takes a permit, waiting under ctx for one to be released. A
// context that is already done fails at once even if a permit is free, so
// a cancelled caller never proceeds by luck.
func (s *Semaphore) Acquire(ctx context.Context) (*Permit, error) {
	if s == nil {
		return nil, &PlanError{Index: -1, Cause: ErrNilRunner}
	}
	if err := ctx.Err(); err != nil {
		return nil, context.Cause(ctx)
	}
	select {
	case s.permits <- struct{}{}:
		return &Permit{permits: s.permits}, nil
	case <-ctx.Done():
		return nil, context.Cause(ctx)
	}
}

// TryAcquire takes a permit if one is free, without waiting.
func (s *Semaphore) TryAcquire() (*Permit, bool) {
	if s == nil {
		return nil, false
	}
	select {
	case s.permits <- struct{}{}:
		return &Permit{permits: s.permits}, true
	default:
		return nil, false
	}
}

// Permit is one held unit of a Semaphore or the lock of a Mutex. Release
// hands it back exactly once; later calls do nothing.
type Permit struct {
	permits chan struct{}
	once    sync.Once
}

// Release hands the permit back. It is idempotent, so a deferred Release
// beside an explicit one is safe.
func (p *Permit) Release() {
	if p == nil {
		return
	}
	p.once.Do(func() { <-p.permits })
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
type Mutex struct {
	sem Semaphore
}

// NewMutex returns an unlocked Mutex.
func NewMutex() *Mutex {
	return &Mutex{sem: Semaphore{permits: make(chan struct{}, 1)}}
}

// Lock takes the lock, waiting under ctx.
func (m *Mutex) Lock(ctx context.Context) (*Permit, error) {
	if m == nil {
		return nil, &PlanError{Index: -1, Cause: ErrNilRunner}
	}
	return m.sem.Acquire(ctx)
}

// TryLock takes the lock if it is free, without waiting.
func (m *Mutex) TryLock() (*Permit, bool) {
	if m == nil {
		return nil, false
	}
	return m.sem.TryAcquire()
}
