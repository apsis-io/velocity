package async

import (
	"context"
	"errors"
	"runtime/debug"
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

// ErrGroup runs functions concurrently under a Runner's Limit, keeps the
// first error, and cancels the group context when it happens, so sibling
// functions that honour their context stop early. It covers everything
// x/sync/errgroup does and what it does not:
//
//   - the Limit is stated once on the Runner and bounds goroutines, not
//     just running work — Go blocks the submitter for a permit;
//
//   - each function receives the group context instead of closing over it;
//
//   - a panic is recovered into a *Panic error rather than taking the
//     process down;
//
//   - a function submitted after the group has failed is not run;
//
//   - Wait returns the first error and Errors every error, in submission
//     order, joined;
//
//   - WaitContext bounds the wait when a function ignores its cancellation;
//
//   - the Runner's Hooks see every function's queueing and run time.
//
//     eg, ctx := run.ErrGroup(ctx)
//     for _, item := range items {
//     eg.Go(func(ctx context.Context) error { return process(ctx, item) })
//     }
//     if err := eg.Wait(); err != nil { ... }
//
// Use Gather or Map when the results matter; ErrGroup is for work where
// only success or the first failure does.
type ErrGroup struct {
	run     *Runner
	ctx     context.Context
	cancel  context.CancelCauseFunc
	permits chan struct{} // nil when unlimited

	wg      sync.WaitGroup
	started atomic.Int64

	mu       sync.Mutex
	errs     []indexedErr
	done     chan struct{} // closed once every started function has returned
	watching bool          // a goroutine is waiting to close done
}

type indexedErr struct {
	index int
	err   error
}

// ErrGroup derives a cancellable context from ctx and returns a group over
// it. The context is cancelled by the first error, by Wait once every
// function has returned, and by the parent.
func (r *Runner) ErrGroup(ctx context.Context) (*ErrGroup, context.Context) {
	ctx, cancel := context.WithCancelCause(ctx)
	g := &ErrGroup{run: r, ctx: ctx, cancel: cancel, done: make(chan struct{})}
	if r != nil && !r.limit.unlimited {
		g.permits = make(chan struct{}, r.limit.value)
	}
	return g, ctx
}

// Go runs fn on its own goroutine, first waiting for a permit if the Runner
// is Limited so that the Limit bounds goroutines, not just running work. A
// function that obtains its permit after the group context is already
// cancelled is not run: the group is failing and its result could not
// change that. Go blocks for the permit regardless, as x/sync does; only
// WaitContext bounds a wait on functions that ignore their cancellation.
func (g *ErrGroup) Go(fn func(context.Context) error) {
	if g.run == nil {
		g.record(-1, &PlanError{Index: -1, Cause: ErrNilRunner})
		return
	}
	if fn == nil {
		index := g.next()
		g.record(index, &PlanError{Index: index, Cause: ErrNilTask})
		return
	}
	var waited time.Duration
	if g.permits != nil {
		var start time.Time
		if g.run.hooks.OnTaskComplete != nil {
			start = time.Now()
		}
		// A plain send, checked afterwards, rather than a select against
		// the group context: the select costs ~250 ns per contended permit
		// and buys only an earlier return for a submitter blocked behind
		// functions that ignore their cancellation, which x/sync does not
		// offer either.
		g.permits <- struct{}{}
		if !start.IsZero() {
			waited = time.Since(start)
		}
		if g.ctx.Err() != nil {
			<-g.permits
			return
		}
	}
	g.start(fn, waited)
}

// TryGo is Go that does not wait for a permit: it reports false, and runs
// nothing, if the Limit is reached or the group context is done.
func (g *ErrGroup) TryGo(fn func(context.Context) error) bool {
	if g.run == nil || fn == nil {
		g.Go(fn)
		return false
	}
	if g.ctx.Err() != nil {
		return false
	}
	if g.permits != nil {
		select {
		case g.permits <- struct{}{}:
		default:
			return false
		}
	}
	g.start(fn, 0)
	return true
}

func (g *ErrGroup) start(fn func(context.Context) error, waited time.Duration) {
	index := g.next()
	// Add/go rather than WaitGroup.Go: the latter wraps f in a second
	// closure, which is an allocation and an indirection per function.
	g.wg.Add(1)
	go g.exec(fn, index, waited)
}

func (g *ErrGroup) exec(fn func(context.Context) error, index int, waited time.Duration) {
	hook := g.run.hooks.OnTaskComplete
	var err error
	var runStart time.Time
	if hook != nil {
		runStart = time.Now()
	}
	defer func() {
		if value := recover(); value != nil {
			err = &Panic{Value: value, Stack: debug.Stack()}
		}
		if err != nil {
			g.record(index, err)
		}
		// Release the permit only after a failure has cancelled the group,
		// so a submitter blocked on it sees the cancellation rather than
		// the free slot.
		if g.permits != nil {
			<-g.permits
		}
		if hook != nil {
			hook(index, "", waited, time.Since(runStart), err)
		}
		g.wg.Done()
	}()
	err = fn(g.ctx)
}

func (g *ErrGroup) next() int { return int(g.started.Add(1) - 1) }

// record keeps every error and cancels the group with the first.
func (g *ErrGroup) record(index int, err error) {
	g.mu.Lock()
	first := len(g.errs) == 0
	g.errs = append(g.errs, indexedErr{index, err})
	g.mu.Unlock()
	if first {
		g.cancel(err)
	}
}

func (g *ErrGroup) first() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.errs) == 0 {
		return nil
	}
	return g.errs[0].err
}

// Wait blocks until every started function has returned, cancels the group
// context, and returns the first error. It may be called more than once.
func (g *ErrGroup) Wait() error {
	g.wg.Wait()
	err := g.first()
	g.cancel(err)
	return err
}

// WaitContext is Wait bounded by ctx, for a function that ignores its
// cancellation: if ctx ends first it returns ctx's cause and the group
// context is cancelled, but the straggler keeps running and a later Wait
// still collects it.
func (g *ErrGroup) WaitContext(ctx context.Context) error {
	g.settle()
	select {
	case <-g.done:
		return g.Wait()
	case <-ctx.Done():
		g.cancel(context.Cause(ctx))
		return context.Cause(ctx)
	}
}

// settle arranges for done to close once the WaitGroup drains. Only
// WaitContext needs it, and only its first caller spawns the watcher; Wait
// blocks on the WaitGroup directly and pays nothing.
func (g *ErrGroup) settle() {
	g.mu.Lock()
	spawn := !g.watching
	g.watching = true
	g.mu.Unlock()
	if spawn {
		go func() { g.wg.Wait(); close(g.done) }()
	}
}

// Errors returns every error the functions reported, in submission order,
// joined, or nil if none did. Call it after Wait; before that it reports
// only what has failed so far.
func (g *ErrGroup) Errors() error {
	g.mu.Lock()
	errs := make([]indexedErr, len(g.errs))
	copy(errs, g.errs)
	g.mu.Unlock()
	if len(errs) == 0 {
		return nil
	}
	slices.SortStableFunc(errs, func(a, b indexedErr) int { return a.index - b.index })
	joined := make([]error, len(errs))
	for i, e := range errs {
		joined[i] = e.err
	}
	return errors.Join(joined...)
}
