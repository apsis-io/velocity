// Package traits holds the vocabulary shared across this repository: the
// function types a resource's lifecycle is described in, and the two types
// that carry the outcome of work — a Result, and a Future for work that has not
// finished.
package traits

import (
	"context"
	"sync"
)

// Result is the outcome of work: succeeded with a value, or failed with an
// error. It is deliberately not a third state. "Not finished yet" is a property
// of the handle, not of the outcome, so it belongs to Future — which is why the
// two are separate types and why a Future that has not resolved has no Result
// rather than a Result holding a sentinel meaning "wait".
//
// The zero Result is a succeeded zero value, and that is the correct reading:
// there is no "absent" state here. A map of Results is aligned to what was
// requested, and a key whose work legitimately produced a zero value is
// indistinguishable from nothing only if something else could be absent — which
// nothing here allows.
type Result[R any] struct {
	Value R
	Err   error
}

// Ok reports whether the work succeeded.
func (r Result[R]) Ok() bool { return r.Err == nil }

// Future is a handle on work that may not have finished. It is either
// unresolved, or it carries a Result.
//
// The distinction a Future exists to keep: **a Result's Err is the work's
// failure, and the error Await returns is the wait's.** A caller that gives up
// waiting has learned something about its own patience, not about the work —
// which is why the two are separate returns rather than one error channel
// carrying both.
type Future[R any] struct {
	done chan struct{}
	once sync.Once

	// Written by whoever completes the future before done is closed, and read
	// by whoever observes the close. The happens-before comes from the close, so
	// nothing guards it and a Future nobody ever reads still completes.
	res Result[R]
}

// NewFuture returns an unresolved Future. Whoever owns the work calls Complete
// exactly once on it.
func NewFuture[R any]() *Future[R] {
	return &Future[R]{done: make(chan struct{})}
}

// Complete resolves the Future. It is safe to call more than once and only the
// first call counts, so a worker that finishes and a deferred cleanup racing to
// signal cannot both win and cannot corrupt.
//
// There is no way to un-resolve a Future, and none is needed: a Future that
// resolved to a failure is finished, not abandoned.
func (f *Future[R]) Complete(value R, err error) {
	if f == nil {
		return
	}

	f.once.Do(func() {
		f.res = Result[R]{Value: value, Err: err}
		close(f.done)
	})
}

// CompleteResult resolves the Future with a Result already in hand.
func (f *Future[R]) CompleteResult(res Result[R]) {
	f.Complete(res.Value, res.Err)
}

// Done returns a channel closed once the work has finished, one way or the
// other. A nil Future is already finished, so its channel is closed and every
// read reports a zero Result — a nil handle has no work behind it, which is not
// the same as work that has not started.
func (f *Future[R]) Done() <-chan struct{} {
	if f == nil {
		return closedDone
	}

	return f.done
}

var closedDone = func() <-chan struct{} {
	ch := make(chan struct{})
	close(ch)

	return ch
}()

// Try returns the Result and whether the work has finished. It never blocks, so
// a caller polling in a loop can starve the goroutine it is polling for; use
// Await or select on Done when the answer matters.
//
// A false second return is not an error and has no sentinel: the outcome is
// unknown, which is a different statement from "the outcome was an error", and
// folding the two into one return is the thing this pair of types exists to
// avoid.
func (f *Future[R]) Try() (Result[R], bool) {
	if f == nil {
		return Result[R]{}, true
	}

	select {
	case <-f.done:
		return f.res, true
	default:
		return Result[R]{}, false
	}
}

// Await waits for the work to finish and returns its Result, or waits for ctx
// to end and returns ctx's cause. The returned error is the **wait's**, not the
// work's: on a timeout the work still runs and the Future still resolves for
// anyone else watching it. That is the property that makes a Future droppable —
// nothing is abandoned, so nothing needs cleaning up by whoever let go.
func (f *Future[R]) Await(ctx context.Context) (Result[R], error) {
	if f == nil {
		return Result[R]{}, nil
	}

	select {
	case <-f.done:
		return f.res, nil
	case <-ctx.Done():
		return Result[R]{}, context.Cause(ctx)
	}
}
