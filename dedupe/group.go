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
	hooks   Hooks[K]
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
	ctx, cancel := context.WithCancel(ctx)
	return &execution{ctx: ctx, cancel: cancel}
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
	exec *execution

	mu        sync.Mutex
	accepting bool
	waiting   int
	left      int
	abandoned bool
	finished  bool
	result    *ownership.Shared[V]
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
	return &Group[K, V]{baseCtx: baseCtx, backend: b, drop: cfg.drop, clone: cfg.clone, hooks: cfg.hooks}, nil
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

// Do executes fn once for each key while sharing successful results.
func (g *Group[K, V]) Do(ctx context.Context, key K, fn func(context.Context) (V, error)) (*ownership.Shared[V], error) {
	if ctx == nil {
		return nil, ErrNilContext
	}
	if fn == nil {
		return nil, ErrNilFunction
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c, leader := g.join(key)
	if leader {
		go g.run(key, c, fn)
	}
	return g.wait(ctx, key, c)
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
		exec := shared
		if exec == nil {
			exec = newExecution(g.baseCtx)
		}
		candidate := &call[V]{done: make(chan struct{}), exec: exec, accepting: true, waiting: 1}
		actual, loaded = g.backend.loadOrStore(key, candidate)
		if !loaded {
			exec.add()
			if g.hooks.OnJoin != nil {
				g.hooks.OnJoin(key, true)
			}
			return candidate, true
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
		calls[key].result, calls[key].err = g.share(value)
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
	c.result, c.err = g.share(value)
	normal = true
}

func capturePanic(target **PanicError) {
	if value := recover(); value != nil && *target == nil {
		*target = &PanicError{Value: value, Stack: debug.Stack()}
	}
}

func (g *Group[K, V]) share(value V) (*ownership.Shared[V], error) {
	owner, err := ownership.New(value, g.resultOptions()...)
	if err != nil {
		return nil, err
	}
	shared, err := owner.IntoShared()
	if err != nil {
		_ = owner.Release()
		return nil, err
	}
	return shared, nil
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
	if c.left == c.waiting && c.result != nil {
		release, c.result = c.result, nil
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

func (g *Group[K, V]) wait(ctx context.Context, key K, c *call[V]) (*ownership.Shared[V], error) {
	select {
	case <-c.done:
	case <-ctx.Done():
		select {
		case <-c.done:
		default:
			g.leave(key, c)
			return nil, ctx.Err()
		}
	}
	c.mu.Lock()
	panicErr, err := c.panicErr, c.err
	var result *ownership.Shared[V]
	if err == nil && panicErr == nil && c.result != nil {
		result, err = c.result.Clone()
	}
	c.mu.Unlock()
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
	if c.finished && c.left == c.waiting && c.result != nil {
		release, c.result = c.result, nil
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
