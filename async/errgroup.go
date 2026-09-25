package async

import (
	"context"
	"errors"
	"runtime/debug"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/apsis-io/velocity/traits"
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
//   - a panic is recovered into a *traits.Panic rather than taking the
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
// change that. Go blocks for the permit regardless, as x/sync does; GoCtx
// is Go for a submitter that must be able to give up on that wait.
//
// Because a function may not run, cleanup it was meant to perform for
// state set up before Go is not performed either. Register such cleanup in
// the Runner's Hooks.OnTaskComplete, which fires for a submission that never
// ran with the cancellation cause, or perform it after Wait for every
// submission.
//
// This is the opposite of what dedupe.Do does with the same situation, and the
// difference is deliberate. A group function is an independent work item: one
// that would run against a dead context is work nobody asked for, so Go
// refuses it. A dedupe callback is *shared* — one execution serving every
// caller on a key — and skipping it would strand each caller arriving
// afterwards on a round that can never produce a value. See the note on Do.
func (g *ErrGroup) Go(fn func(context.Context) error) {
	if !g.admissible(fn) {
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
			g.skipped(waited)

			return
		}
	}

	g.start(fn, waited)
}

// GoCtx is Go with the permit wait bounded by ctx, and it reports whether
// fn was submitted. False means the group was already finished, or finished
// while the submitter waited, and fn never ran.
//
// This is the difference between a group and a stream. Go's permit wait is an
// unbounded send, which is the right trade for a submitter that has work it
// must run and no reason to stop: the ~250 ns it saves per contended permit is
// paid by every caller, while an earlier return is wanted by some. A consumer
// reading from a channel is the other case — it is the other end of a
// producer that may still be publishing, it holds a shutdown context, and it
// must be able to stop reading. With every permit held by a function that
// ignores its own cancellation, a Go loop cannot reach its cancellation branch
// at all, and so never reaches WaitContext either: the documented way to bound
// exactly this wait is unreachable from the shape that needs it.
//
// ctx bounds the wait for a permit and nothing else. fn still receives the
// group context, which is the one Wait and cancellation speak about.
func (g *ErrGroup) GoCtx(ctx context.Context, fn func(context.Context) error) bool {
	if ctx == nil {
		if g.run == nil {
			g.record(-1, &TaskError{Index: -1, Cause: ErrNilReceiver})
			return false
		}

		index := g.next()
		g.record(index, &TaskError{Index: index, Cause: ErrNilContext})

		return false
	}

	if !g.admissible(fn) {
		return false
	}

	if err := ctx.Err(); err != nil {
		g.skipped(0)
		return false
	}

	var waited time.Duration

	if g.permits != nil {
		var start time.Time
		if g.run.hooks.OnTaskComplete != nil {
			start = time.Now()
		}

		select {
		case g.permits <- struct{}{}:
		case <-ctx.Done():
			if !start.IsZero() {
				waited = time.Since(start)
			}

			g.skipped(waited)

			return false
		}

		if !start.IsZero() {
			waited = time.Since(start)
		}

		if g.ctx.Err() != nil {
			<-g.permits
			g.skipped(waited)

			return false
		}
	}

	g.start(fn, waited)

	return true
}

// admissible reports whether a submission is well formed, recording the
// error for one that is not. Both Go and GoCtx start here, so a malformed
// submission is reported the same way whichever was called.
func (g *ErrGroup) admissible(fn func(context.Context) error) bool {
	if g.run == nil {
		g.record(-1, &TaskError{Index: -1, Cause: ErrNilReceiver})
		return false
	}

	if fn == nil {
		index := g.next()
		g.record(index, &TaskError{Index: index, Cause: ErrNilTask})

		return false
	}

	return true
}

// skipped reports a submission that will not run. Map already reports the
// items its workers never claimed, with the cancellation cause; a group that
// discarded a submission silently would leave a consumer draining a stream
// unable to account for what it read, with Wait returning nil.
func (g *ErrGroup) skipped(waited time.Duration) {
	index := g.next()
	if hook := g.run.hooks.OnTaskComplete; hook != nil {
		hook(index, "", waited, 0, context.Cause(g.ctx))
	}
}

// TryGo is Go that does not wait for a permit: it reports false, and runs
// nothing, if the Limit is reached or the group context is done.
func (g *ErrGroup) TryGo(fn func(context.Context) error) bool {
	if g.run == nil || fn == nil {
		g.Go(fn)
		return false
	}

	if g.ctx.Err() != nil {
		g.skipped(0)
		return false
	}

	if g.permits != nil {
		select {
		case g.permits <- struct{}{}:
		default:
			// Refused for want of a permit, not for cancellation: the caller
			// asked not to wait and is told so by this return, and a
			// submission it chose not to make is not work that went missing.
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

	var (
		err      error
		runStart time.Time
	)
	if hook != nil {
		runStart = time.Now()
	}

	defer func() {
		if value := recover(); value != nil {
			err = &traits.Panic{Value: value, Stack: debug.Stack()}
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
