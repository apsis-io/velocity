package dedupe

import (
	"context"
	"runtime/debug"
	"sync"
	"time"

	"github.com/apsis-io/velocity/ownership"
)

type Group[K comparable, V any] struct {
	baseCtx context.Context
	backend backend[K, V]
	drop    func(V) error
	clone   func(V) (V, error)
	// owned means every round builds one ownership cell over its result and
	// hands out counted handles, so Drop runs when the last caller releases.
	// Such a group serves results only through DoShared.
	owned bool
	hooks Hooks[K]
}

// Singleflight is an exact alias of Group for readers who know this pattern
// by its more common name.
type Singleflight[K comparable, V any] = Group[K, V]

// NewSingleflight is an exact alias of New.
func NewSingleflight[K comparable, V any](baseCtx context.Context, opts ...Option[K, V]) (*Group[K, V], error) {
	return New(baseCtx, opts...)
}

type execution struct {
	ctx    context.Context
	cancel context.CancelFunc
	mu     sync.Mutex
	active int
}

func newExecution(ctx context.Context) *execution {
	e := &execution{}
	e.init(ctx)
	return e
}

func (e *execution) init(parent context.Context) {
	e.ctx, e.cancel = context.WithCancel(parent)
}

func (e *execution) add() {
	e.mu.Lock()
	e.active++
	e.mu.Unlock()
}

func (e *execution) abandon() {
	e.mu.Lock()
	e.active--
	if e.active == 0 {
		e.cancel()
	}
	e.mu.Unlock()
}

type call[V any] struct {
	done chan struct{}
	// exec points at own for a single-key round, or at the execution a batch
	// shares between its keys. Embedding own saves an allocation per round.
	exec *execution
	own  execution

	mu        sync.Mutex
	accepting bool
	waiting   int
	left      int
	abandoned bool
	finished  bool
	value     V
	cell      *ownership.Shared[V] // owned groups only
	err       error
	panicErr  *PanicError
}

// New constructs a duplicate-suppressing group.
func New[K comparable, V any](baseCtx context.Context, opts ...Option[K, V]) (*Group[K, V], error) {
	if baseCtx == nil {
		return nil, &ConfigError{Option: "base context", Cause: ErrNilContext}
	}
	cfg := config[K, V]{}
	for i, opt := range opts {
		if opt == nil {
			return nil, &ConfigError{Option: "option " + formatIndex(i), Cause: ErrNilOption}
		}
		if err := opt.apply(&cfg); err != nil {
			return nil, err
		}
	}
	var b backend[K, V]
	switch cfg.backendKind {
	case backendMutex:
		b = newMutexBackend[K, V]()
	case backendSharded:
		b = newShardedBackend[K, V](cfg.shards)
	case backendXsync:
		b = newXsyncBackend[K, V]()
	default:
		return nil, &ConfigError{Option: "backend", Cause: ErrUnsupportedBackend}
	}
	return &Group[K, V]{
		baseCtx: baseCtx,
		backend: b,
		drop:    cfg.drop,
		clone:   cfg.clone,
		owned:   cfg.drop != nil || cfg.clone != nil,
		hooks:   cfg.hooks,
	}, nil
}

func formatIndex(i int) string {
	var buf [20]byte
	pos := len(buf)
	if i == 0 {
		return "0"
	}
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(buf[pos:])
}

// Do runs fn once per key for every concurrent caller and hands each of them
// the value. It is the plain form: the result is copied out, nothing is
// tracked, and a group configured with WithResultDrop or WithResultClone
// refuses it with ErrOwnedResult, because a bare copy would escape Drop.
func (g *Group[K, V]) Do(ctx context.Context, key K, fn func(context.Context) (V, error)) (V, error) {
	var zero V
	if err := g.checkPlain(ctx, fn); err != nil {
		return zero, err
	}
	c, leader := g.join(key)
	if leader {
		go g.run(key, c, fn)
	}
	return g.wait(ctx, key, c)
}

// DoShared is Do returning an independently released ownership handle per
// caller. On an owned group every caller's handle counts toward one cell,
// and the group's Drop runs when the last of them releases; on a plain group
// each handle is its own cell over a copy, with nothing to drop.
func (g *Group[K, V]) DoShared(ctx context.Context, key K, fn func(context.Context) (V, error)) (*ownership.Shared[V], error) {
	if err := g.check(ctx, fn); err != nil {
		return nil, err
	}
	c, leader := g.join(key)
	if leader {
		go g.run(key, c, fn)
	}
	return g.waitShared(ctx, key, c)
}

func (g *Group[K, V]) check(ctx context.Context, fn func(context.Context) (V, error)) error {
	if ctx == nil {
		return ErrNilContext
	}
	if fn == nil {
		return ErrNilFunction
	}
	return ctx.Err()
}

func (g *Group[K, V]) checkPlain(ctx context.Context, fn func(context.Context) (V, error)) error {
	if g.owned {
		return ErrOwnedResult
	}
	return g.check(ctx, fn)
}

func (g *Group[K, V]) join(key K) (*call[V], bool) {
	return g.joinWithExecution(key, nil)
}

func (g *Group[K, V]) joinWithExecution(key K, shared *execution) (*call[V], bool) {
	for {
		actual, loaded := g.backend.load(key)
		if loaded {
			actual.mu.Lock()
			if actual.accepting {
				actual.waiting++
				actual.mu.Unlock()
				if g.hooks.OnJoin != nil {
					g.hooks.OnJoin(key, false)
				}
				return actual, false
			}
			actual.mu.Unlock()
			continue
		}
		candidate := &call[V]{done: make(chan struct{}), exec: shared, accepting: true, waiting: 1}
		if shared == nil {
			candidate.own.init(g.baseCtx)
			candidate.exec = &candidate.own
		}
		actual, loaded = g.backend.loadOrStore(key, candidate)
		if !loaded {
			candidate.exec.add()
			if g.hooks.OnJoin != nil {
				g.hooks.OnJoin(key, true)
			}
			return candidate, true
		}
		if shared == nil {
			// Lost the race to register: the context derived for this
			// candidate would otherwise stay registered with baseCtx.
			candidate.own.cancel()
		}
		actual.mu.Lock()
		if actual.accepting {
			actual.waiting++
			actual.mu.Unlock()
			if g.hooks.OnJoin != nil {
				g.hooks.OnJoin(key, false)
			}
			return actual, false
		}
		actual.mu.Unlock()
	}
}

func (g *Group[K, V]) runBatch(keys []K, calls map[K]*call[V], fn func(context.Context, []K) (map[K]V, error)) {
	ctx := calls[keys[0]].exec.ctx
	start := time.Now()
	defer func() {
		if recovered := recover(); recovered != nil {
			for _, key := range keys {
				calls[key].panicErr = &PanicError{Value: recovered, Stack: debug.Stack()}
			}
		}
		for _, key := range keys {
			g.complete(key, calls[key], start)
		}
	}()
	values, err := fn(ctx, keys)
	if err != nil {
		for _, key := range keys {
			calls[key].err = err
		}
		return
	}
	for _, key := range keys {
		value, ok := values[key]
		if !ok {
			calls[key].err = ErrMissingResult
			continue
		}
		calls[key].value = value
	}
}

func (g *Group[K, V]) run(key K, c *call[V], fn func(context.Context) (V, error)) {
	start := time.Now()
	normal := false
	var panicErr *PanicError
	defer func() {
		if !normal && panicErr == nil {
			c.err = ErrCallbackExit
		}
		c.panicErr = panicErr
		g.complete(key, c, start)
	}()
	defer capturePanic(&panicErr)

	value, err := fn(c.exec.ctx)
	if err != nil {
		c.err = err
		normal = true
		return
	}
	c.value = value
	if g.owned {
		c.cell, c.err = g.share(value)
	}
	normal = true
}

func capturePanic(target **PanicError) {
	if value := recover(); value != nil && *target == nil {
		*target = &PanicError{Value: value, Stack: debug.Stack()}
	}
}

func (g *Group[K, V]) share(value V) (*ownership.Shared[V], error) {
	return ownership.NewShared(value, g.resultOptions()...)
}

func (g *Group[K, V]) resultOptions() []ownership.Option[V] {
	opts := make([]ownership.Option[V], 0, 2)
	if g.drop != nil {
		opts = append(opts, ownership.WithDrop(g.drop))
	}
	if g.clone != nil {
		opts = append(opts, ownership.WithClone(g.clone))
	}
	return opts
}

func (g *Group[K, V]) complete(key K, c *call[V], start time.Time) {
	c.mu.Lock()
	c.accepting = false
	c.finished = true
	var release *ownership.Shared[V]
	if c.left == c.waiting && c.cell != nil {
		release, c.cell = c.cell, nil
	}
	hookErr := c.err
	if hookErr == nil && c.panicErr != nil {
		hookErr = c.panicErr
	}
	c.mu.Unlock()
	g.backend.compareAndDelete(key, c)
	close(c.done)
	c.exec.cancel()
	if g.hooks.OnComplete != nil {
		g.hooks.OnComplete(key, time.Since(start), hookErr)
	}
	if release != nil {
		_ = release.Release()
	}
}

// await blocks until the round completes or ctx is done. It reports whether
// the round completed; on false the caller has already left.
func (g *Group[K, V]) await(ctx context.Context, key K, c *call[V]) bool {
	select {
	case <-c.done:
		return true
	case <-ctx.Done():
		select {
		case <-c.done:
			return true
		default:
			g.leave(key, c)
			return false
		}
	}
}

func (g *Group[K, V]) wait(ctx context.Context, key K, c *call[V]) (V, error) {
	var zero V
	if !g.await(ctx, key, c) {
		return zero, ctx.Err()
	}
	// The round is complete, so its fields are immutable now; the value is
	// copied before leaving, since leaving may release the round.
	panicErr, err, value := c.panicErr, c.err, c.value
	g.leave(key, c)
	if panicErr != nil {
		panic(panicErr)
	}
	if err != nil {
		return zero, err
	}
	return value, nil
}

func (g *Group[K, V]) waitShared(ctx context.Context, key K, c *call[V]) (*ownership.Shared[V], error) {
	if !g.await(ctx, key, c) {
		return nil, ctx.Err()
	}
	panicErr, err := c.panicErr, c.err
	var result *ownership.Shared[V]
	if err == nil && panicErr == nil {
		if c.cell != nil {
			// Clone under the lock: leave may release the cell concurrently
			// once every other caller has gone.
			c.mu.Lock()
			if c.cell != nil {
				result, err = c.cell.Clone()
			} else {
				err = ownership.ErrReleased
			}
			c.mu.Unlock()
		} else {
			result, err = ownership.NewShared(c.value)
		}
	}
	g.leave(key, c)
	if panicErr != nil {
		panic(panicErr)
	}
	return result, err
}

func (g *Group[K, V]) leave(key K, c *call[V]) {
	abandon := false
	c.mu.Lock()
	c.left++
	if c.left == c.waiting && !c.finished && !c.abandoned {
		c.abandoned = true
		c.accepting = false
		abandon = true
	}
	var release *ownership.Shared[V]
	if c.finished && c.left == c.waiting && c.cell != nil {
		release, c.cell = c.cell, nil
	}
	c.mu.Unlock()
	if abandon {
		c.exec.abandon()
		// Remove only this generation; a concurrent replacement is preserved.
		g.backend.compareAndDelete(key, c)
	}
	if release != nil {
		_ = release.Release()
	}
}

// Forget stops tracking key without interrupting work already in progress.
func (g *Group[K, V]) Forget(key K) bool { _, ok := g.backend.delete(key); return ok }

// Cancel asks the in-flight operation for a key to stop.
func (g *Group[K, V]) Cancel(key K) bool {
	c, ok := g.backend.load(key)
	if !ok {
		return false
	}
	c.exec.cancel()
	return true
}
