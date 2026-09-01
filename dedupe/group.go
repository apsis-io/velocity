package dedupe

import (
	"context"
	"runtime/debug"
	"sync"
	"time"

	"github.com/apsis-io/velocity/ownership"
)

type Group[K comparable, V any] struct {
	// once installs defaults into a zero-value Group on first use, so a
	// Group field in a struct literal works as singleflight.Group does.
	once    sync.Once
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
// by its more common name. It is not a drop-in for x/sync/singleflight in
// one respect: a round whose callers have all left has its context
// cancelled, and a later caller takes its value only if it still succeeded,
// starting afresh otherwise; x/sync hands every joiner whatever the round
// eventually returns. Both keep at most one callback in flight per key.
type Singleflight[K comparable, V any] = Group[K, V]

// NewSingleflight is an exact alias of New.
func NewSingleflight[K comparable, V any](opts ...Option[K, V]) (*Group[K, V], error) {
	return New(opts...)
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

// New constructs a duplicate-suppressing group. Work runs under a context
// derived from context.Background by default, so it outlives any one
// caller; WithBaseContext changes the parent.
//
// The zero value is also a usable Group with every default, so a Group can
// be a plain struct field; New is needed only to pass options.
func New[K comparable, V any](opts ...Option[K, V]) (*Group[K, V], error) {
	g, err := build[K, V](opts)
	if err != nil {
		return nil, err
	}
	g.once.Do(func() {}) // configured: never apply defaults over it
	return g, nil
}

// Must is New for a fixed option list that cannot fail, in the manner of
// regexp.MustCompile: it panics on error, for package-level and constructor
// use where there is nowhere to return one.
func Must[K comparable, V any](g *Group[K, V], err error) *Group[K, V] {
	if err != nil {
		panic(err)
	}
	return g
}

// ready installs defaults into a zero-value Group exactly once.
func (g *Group[K, V]) ready() {
	g.once.Do(func() {
		built, _ := build[K, V](nil) // no options: cannot fail
		g.baseCtx, g.backend, g.hooks = built.baseCtx, built.backend, built.hooks
	})
}

func build[K comparable, V any](opts []Option[K, V]) (*Group[K, V], error) {
	cfg := config[K, V]{baseCtx: context.Background()}
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
		baseCtx: cfg.baseCtx,
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
//
// The context fn receives belongs to the round and is cancelled when the
// round completes. A value that keeps doing work after Do returns — a lazy
// handle that issues requests on later reads, a stream, anything holding
// the context — must therefore not be built from fn's context; derive it
// from the caller's, or from the group's base context, instead. Binding it
// to the round's context fails silently until the first later use.
func (g *Group[K, V]) Do(ctx context.Context, key K, fn func(context.Context) (V, error)) (V, error) {
	var zero V
	if err := g.checkPlain(ctx, fn); err != nil {
		return zero, err
	}
	c, leader, err := g.join(ctx, key)
	if err != nil {
		return zero, err
	}
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
	c, leader, err := g.join(ctx, key)
	if err != nil {
		return nil, err
	}
	if leader {
		go g.run(key, c, fn)
	}
	return g.waitShared(ctx, key, c)
}

func (g *Group[K, V]) check(ctx context.Context, fn func(context.Context) (V, error)) error {
	g.ready()
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

func (g *Group[K, V]) join(ctx context.Context, key K) (*call[V], bool, error) {
	return g.joinWithExecution(ctx, key, nil)
}

// joinWithExecution registers the caller on the key's current round or
// starts one.
//
// A round every caller has abandoned has its work cancelled, but its key
// stays registered until the callback actually returns, and a caller
// arriving meanwhile waits for that under its own context. If the callback
// then produced a value, the caller takes it — a callback that ignored its
// cancellation did the work, so it is not repeated; if it failed, which for
// a cooperative callback means it reported the cancellation the caller had
// no part in, the caller starts a fresh round instead. Either way at most
// one callback is in flight per key, which is what singleflight users rely
// on, and no caller ever receives another caller's cancellation. Forget
// releases the key for a caller who wants a fresh round at once.
func (g *Group[K, V]) joinWithExecution(ctx context.Context, key K, shared *execution) (*call[V], bool, error) {
	for {
		actual, loaded := g.backend.load(key)
		if loaded {
			if joined := g.tryJoin(key, actual); joined {
				return actual, false, nil
			}
			adopted, err := g.awaitRetired(ctx, key, actual)
			if err != nil {
				return nil, false, err
			}
			if adopted {
				return actual, false, nil
			}
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
			return candidate, true, nil
		}
		if shared == nil {
			// Lost the race to register: the context derived for this
			// candidate would otherwise stay registered with baseCtx.
			candidate.own.cancel()
		}
		if joined := g.tryJoin(key, actual); joined {
			return actual, false, nil
		}
		adopted, err := g.awaitRetired(ctx, key, actual)
		if err != nil {
			return nil, false, err
		}
		if adopted {
			return actual, false, nil
		}
	}
}

// tryJoin adds the caller to a round that is still accepting.
func (g *Group[K, V]) tryJoin(key K, c *call[V]) bool {
	c.mu.Lock()
	if !c.accepting {
		c.mu.Unlock()
		return false
	}
	c.waiting++
	c.mu.Unlock()
	if g.hooks.OnJoin != nil {
		g.hooks.OnJoin(key, false)
	}
	return true
}

// awaitRetired blocks until an abandoned round's callback has returned, or
// ctx is done. It then adopts the round — registering the caller as one more
// recipient — if the callback succeeded and the value is a plain copy; an
// owned round's cell was released with its last caller and cannot be
// re-shared. Otherwise the caller retries and starts afresh once complete
// has unregistered the key.
func (g *Group[K, V]) awaitRetired(ctx context.Context, key K, c *call[V]) (adopted bool, err error) {
	select {
	case <-c.done:
	case <-ctx.Done():
		return false, context.Cause(ctx)
	}
	if g.owned {
		return false, nil
	}
	c.mu.Lock()
	if c.err == nil && c.panicErr == nil {
		c.waiting++
		adopted = true
	}
	c.mu.Unlock()
	if adopted && g.hooks.OnJoin != nil {
		g.hooks.OnJoin(key, false)
	}
	return adopted, nil
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
		// Cancel the work but leave the key registered: complete removes it
		// once the callback returns, and until then a new caller waits in
		// join rather than starting a second callback for the same key.
		c.exec.abandon()
	}
	if release != nil {
		_ = release.Release()
	}
}

// Forget stops tracking key without interrupting work already in progress,
// so the next caller starts a fresh round even if an abandoned callback for
// the key is still running.
func (g *Group[K, V]) Forget(key K) bool {
	g.ready()
	_, ok := g.backend.delete(key)
	return ok
}

// Cancel asks the in-flight operation for a key to stop.
func (g *Group[K, V]) Cancel(key K) bool {
	g.ready()
	c, ok := g.backend.load(key)
	if !ok {
		return false
	}
	c.exec.cancel()
	return true
}
