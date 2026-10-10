package pool

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"github.com/apsis-io/velocity/ownership"
	"github.com/vburenin/nsync"
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
	cfg Config[T]
	// permits is one slot per resource that may exist. Acquiring it is the
	// capacity wait; releasing it is the last thing a put does, so a waiter
	// admitted by a release finds the idle set already updated.
	permits *nsync.Semaphore

	mu     nsync.TryMutex
	idle   []T
	total  int // idle plus checked out
	closed bool

	// The lifecycle counters Stats reports. Atomics because Get and put are
	// the hot paths they measure — a counter that cost a lock on the path
	// it measures would distort the number it reports.
	created      atomic.Uint64 // constructed resources (pool misses)
	passesServed atomic.Uint64 // checkouts handed out
	retired      atomic.Uint64 // resources destroyed
	failedPasses atomic.Uint64 // checkouts discarded by the caller
}

// New validates cfg and returns an empty pool. Resources are made on demand.
func New[T any](cfg Config[T]) (*Pool[T], error) {
	if cfg.New == nil {
		return nil, &ConfigError{Option: "New", Reason: ownership.ErrNilOption}
	}

	if cfg.Max <= 0 {
		return nil, &ConfigError{Option: "Max", Reason: ErrInvalidMax}
	}

	return &Pool[T]{cfg: cfg, permits: nsync.NewSemaphore(cfg.Max), mu: *nsync.NewTryMutex()}, nil
}

// Must is New for a Config that cannot fail, in the manner of
// regexp.MustCompile: it panics on error.
func Must[T any](p *Pool[T], err error) *Pool[T] {
	if err != nil {
		panic(err)
	}

	return p
}

// Stats is a point-in-time view of the pool: the capacity gauges, and the
// lifecycle counters since New.
type Stats struct {
	// Idle resources ready for the next Get.
	Idle int
	// InUse resources currently checked out.
	InUse int
	// Max is the configured bound on Idle+InUse.
	Max int

	// Created is the resources constructed because a Get found nothing
	// idle — the pool misses.
	Created uint64
	// PassesServed is the checkouts handed out.
	PassesServed uint64
	// Retired is the resources destroyed: discarded by a caller, or
	// destroyed because the pool closed while they were idle.
	Retired uint64
	// FailedPasses is the checkouts a caller discarded — found unusable.
	FailedPasses uint64
}

// ReuseRate is the fraction of checkouts served by a resource that already
// existed. Near 1 means the pool is earning its keep; near 0 means the
// workload's concurrency reaches Max and every Get constructs, or callers
// discard what they get.
func (s Stats) ReuseRate() float64 {
	if s.PassesServed == 0 {
		return 0
	}

	return float64(s.PassesServed-s.Created) / float64(s.PassesServed)
}

// Stats reports the capacity gauges and the lifecycle counters. Safe for
// concurrent use: the gauges are read under the pool lock, the counters
// atomically — a snapshot, not a synchronized instant, with each field
// consistent with itself.
func (p *Pool[T]) Stats() Stats {
	p.mu.Lock()
	defer p.mu.Unlock()

	return Stats{
		Idle:         len(p.idle),
		InUse:        p.total - len(p.idle),
		Max:          p.cfg.Max,
		Created:      p.created.Load(),
		PassesServed: p.passesServed.Load(),
		Retired:      p.retired.Load(),
		FailedPasses: p.failedPasses.Load(),
	}
}

// Checkout is one held resource. It is an ownership.Lease, so Value reports
// ErrReleased once returned, Release returns the resource exactly once, and
// Close is Release — which also makes a Checkout an io.Closer a Scope can
// own. Discard is the one addition: a resource the caller found broken is
// closed instead of returned, and its capacity freed for a fresh one.
type Checkout[T any] struct {
	ownership.Lease[T]
	// p is the pool this checkout came from; the Lease's release handler is
	// this checkout itself, so the value can come back without a per-cycle
	// closure.
	p *Pool[T]
	// discard points at flag, or at the original handle's flag after a Move,
	// so the release handler and every handle agree on it.
	discard *atomic.Bool
	flag    atomic.Bool
}

// ReleaseLease is the Checkout's LeaseHandler half: the Lease calls it with
// the value when the checkout releases. It reads this checkout's discard
// flag, which after a Move is shared with the original handle — see Move.
func (c *Checkout[T]) ReleaseLease(value T) error {
	return c.p.put(value, c.flag.Load())
}

// Move transfers the checkout to a fresh handle and spends this one, exactly
// as Lease.Move, keeping Discard available on the new handle.
func (c *Checkout[T]) Move() (*Checkout[T], error) {
	if c == nil {
		return nil, &ownership.ReleasedError{Operation: ownership.OpMove}
	}

	next := &Checkout[T]{discard: c.discard}
	if err := c.MoveInto(&next.Lease); err != nil {
		return nil, err
	}

	return next, nil
}

// Discard closes the resource rather than returning it to the pool, for a
// caller that found it unusable. Like Release it is exactly-once: after a
// Release it does nothing and returns that Release's result.
func (c *Checkout[T]) Discard() error {
	if c == nil {
		return nil
	}

	// Only a holder may arm the discard. The flag is shared with every
	// handle this one moved to — that is how Discard stays available on the
	// moved-to handle — so a spent handle storing on it would silently
	// convert that handle's Release into a destroy.
	if c.Held() {
		c.discard.Store(true)
	}

	return c.Release()
}

// Get checks out a resource, reusing an idle one or constructing a new one
// when under Max, and otherwise waiting for capacity until ctx is done.
// The returned Checkout must be released or discarded exactly once.
//
//velocity:acquires
func (p *Pool[T]) Get(ctx context.Context) (*Checkout[T], error) {
	start := time.Now()

	checkout, created, err := p.get(ctx)
	if err == nil {
		p.passesServed.Add(1)

		if created {
			p.created.Add(1)
		}
	}

	if hook := p.cfg.Hooks.OnAcquire; hook != nil {
		hook(time.Since(start), created, err)
	}

	return checkout, err
}

func (p *Pool[T]) get(ctx context.Context) (*Checkout[T], bool, error) {
	// A free permit is taken even when ctx is already done — a Get that can
	// return instantly should — and waiting honours the context. Either way
	// the pool reports the context's cause, not the semaphore's error shape.
	if !p.permits.TryAcquire() {
		if err := p.permits.AcquireContext(ctx); err != nil {
			return nil, false, context.Cause(ctx)
		}
	}
	// From here the permit is held; every exit either keeps it in the
	// checkout or hands it back.
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		p.permits.Release()

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
		p.permits.Release()
		return nil, true, err
	}

	p.mu.Lock()
	if p.closed {
		// Closed while constructing; the pool will never hand this out.
		p.mu.Unlock()
		p.permits.Release()

		return nil, true, errors.Join(ErrClosed, p.destroy(value))
	}

	p.total++
	p.mu.Unlock()

	return p.checkout(value), true, nil
}

func (p *Pool[T]) checkout(value T) *Checkout[T] {
	c := &Checkout[T]{p: p}
	c.discard = &c.flag
	// The Lease is embedded by value and this checkout is its release
	// handler, so a checkout is one allocation where the closure form was
	// two.
	_ = ownership.NewLeaseIntoHandler(&c.Lease, value, c)

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
		p.permits.Release()
		p.released(false, nil)

		return nil
	}

	p.total--
	p.mu.Unlock()

	if discard {
		p.failedPasses.Add(1)
	}

	p.retired.Add(1)

	err := p.destroy(value)
	p.permits.Release()
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
		p.retired.Add(1)

		if err := p.destroy(value); err != nil {
			errs = append(errs, err)
		}
	}

	return errors.Join(errs...)
}
