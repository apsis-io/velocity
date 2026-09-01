package pool

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/apsis-io/velocity/ownership"
)

// Config describes how a Pool makes and unmakes resources. New and Max are
// required.
type Config[T any] struct {
	// New constructs a resource when a Get finds nothing idle and the pool
	// is below Max. It runs on the calling goroutine, outside the pool lock.
	New func(context.Context) (T, error)
	// Close destroys a resource the pool is done with: on Pool.Close, or when
	// a checkout is discarded. Nil means resources need no teardown.
	Close func(T) error
	// Max bounds resources in existence, idle and checked out together. Get
	// waits for capacity once it is reached.
	Max   int
	Hooks Hooks
}

// Pool is a bounded set of resources checked out one at a time. Resources
// are reused most-recently-returned first, so a warm one is preferred and a
// cold one drifts toward the bottom of the idle set.
//
// Get waits for capacity and is the only operation that waits; it does so
// under the caller's context. Releasing never blocks.
type Pool[T any] struct {
	cfg     Config[T]
	permits chan struct{} // one per resource that may exist

	mu     sync.Mutex
	idle   []T
	total  int // idle plus checked out
	closed bool
}

// New validates cfg and returns an empty pool. Resources are made on demand.
func New[T any](cfg Config[T]) (*Pool[T], error) {
	if cfg.New == nil {
		return nil, &ConfigError{Field: "New", Reason: ownership.ErrNilOption}
	}
	if cfg.Max <= 0 {
		return nil, &ConfigError{Field: "Max", Reason: ErrInvalidMax}
	}
	return &Pool[T]{cfg: cfg, permits: make(chan struct{}, cfg.Max)}, nil
}

// Checkout is one held resource. It is an ownership.Lease, so Value reports
// ErrReleased once returned, Release returns the resource exactly once, and
// Close is Release — which also makes a Checkout an io.Closer a Scope can
// own. Discard is the one addition: a resource the caller found broken is
// closed instead of returned, and its capacity freed for a fresh one.
type Checkout[T any] struct {
	*ownership.Lease[T]
	// discard points at flag, or at the original handle's flag after a Move,
	// so the release closure and every handle agree on it.
	discard *atomic.Bool
	flag    atomic.Bool
}

// Move transfers the checkout to a fresh handle and spends this one, exactly
// as Lease.Move, keeping Discard available on the new handle.
func (c *Checkout[T]) Move() (*Checkout[T], error) {
	if c == nil {
		return nil, &ownership.ReleasedError{Operation: ownership.OpMove}
	}
	lease, err := c.Lease.Move()
	if err != nil {
		return nil, err
	}
	return &Checkout[T]{Lease: lease, discard: c.discard}, nil
}

// Discard closes the resource rather than returning it to the pool, for a
// caller that found it unusable. Like Release it is exactly-once: after a
// Release it does nothing and returns that Release's result.
func (c *Checkout[T]) Discard() error {
	if c == nil {
		return nil
	}
	c.discard.Store(true)
	return c.Release()
}

// Get checks out a resource, reusing an idle one or constructing a new one
// when under Max, and otherwise waiting for capacity until ctx is done.
// The returned Checkout must be released or discarded exactly once.
func (p *Pool[T]) Get(ctx context.Context) (*Checkout[T], error) {
	start := time.Now()
	checkout, created, err := p.get(ctx)
	if hook := p.cfg.Hooks.OnAcquire; hook != nil {
		hook(time.Since(start), created, err)
	}
	return checkout, err
}

func (p *Pool[T]) get(ctx context.Context) (*Checkout[T], bool, error) {
	select {
	case p.permits <- struct{}{}:
	case <-ctx.Done():
		return nil, false, context.Cause(ctx)
	}
	// From here the permit is held; every exit either keeps it in the
	// checkout or hands it back.
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		<-p.permits
		return nil, false, ErrClosed
	}
	if n := len(p.idle); n > 0 {
		value := p.idle[n-1]
		var zero T
		p.idle[n-1] = zero
		p.idle = p.idle[:n-1]
		p.mu.Unlock()
		return p.checkout(value), false, nil
	}
	p.mu.Unlock()

	value, err := p.cfg.New(ctx)
	if err != nil {
		<-p.permits
		return nil, true, err
	}
	p.mu.Lock()
	if p.closed {
		// Closed while constructing; the pool will never hand this out.
		p.mu.Unlock()
		<-p.permits
		return nil, true, errors.Join(ErrClosed, p.destroy(value))
	}
	p.total++
	p.mu.Unlock()
	return p.checkout(value), true, nil
}

func (p *Pool[T]) checkout(value T) *Checkout[T] {
	c := &Checkout[T]{}
	c.discard = &c.flag
	// NewLease rejects only a nil release, which this is not.
	c.Lease, _ = ownership.NewLease(value, func(value T) error {
		return p.put(value, c.flag.Load())
	})
	return c
}

// put ends a checkout: back to idle, or destroyed if discarded or the pool
// has closed meanwhile. Either way the permit is returned last, so a waiter
// admitted by it finds the idle set already updated.
func (p *Pool[T]) put(value T, discard bool) error {
	p.mu.Lock()
	if !discard && !p.closed {
		p.idle = append(p.idle, value)
		p.mu.Unlock()
		<-p.permits
		p.released(false, nil)
		return nil
	}
	p.total--
	p.mu.Unlock()
	err := p.destroy(value)
	<-p.permits
	p.released(true, err)
	return err
}

func (p *Pool[T]) released(discarded bool, err error) {
	if hook := p.cfg.Hooks.OnRelease; hook != nil {
		hook(discarded, err)
	}
}

func (p *Pool[T]) destroy(value T) error {
	if p.cfg.Close == nil {
		return nil
	}
	return p.cfg.Close(value)
}

// Stats is a point-in-time view of the pool.
type Stats struct {
	// Idle resources ready for the next Get.
	Idle int
	// InUse resources currently checked out.
	InUse int
	// Max is the configured bound on Idle+InUse.
	Max int
}

// Stats reports how many resources exist and how many are checked out.
func (p *Pool[T]) Stats() Stats {
	p.mu.Lock()
	defer p.mu.Unlock()
	return Stats{Idle: len(p.idle), InUse: p.total - len(p.idle), Max: p.cfg.Max}
}

// Close destroys every idle resource and refuses further Gets. It does not
// wait for outstanding checkouts: each of those is destroyed when released,
// so a pool is fully torn down once its last checkout ends. Errors from
// Close callbacks are joined. Close is idempotent.
func (p *Pool[T]) Close() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	idle := p.idle
	p.idle = nil
	p.total -= len(idle)
	p.mu.Unlock()

	var errs []error
	for _, value := range idle {
		if err := p.destroy(value); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
