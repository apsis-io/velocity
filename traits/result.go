// Package traits holds the vocabulary shared across this repository: the
// function types a resource's lifecycle is described in, and the two types
// that carry the outcome of work — a Result, and a Future for work that has not
// finished.
package traits

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// ErrNilContext is what Future.Await returns for a nil context. A Future is
// often carried by a caller that has no context of its own, and the panic a nil
// ctx.Done() would produce is a poor answer to that.
var ErrNilContext = errors.New("traits: nil context")

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
// failure, and a timeout is the wait's** — and Await's error reports whichever
// of the two applies, wrapping the work's so `if err != nil` catches a failure
// without a caller digging into the Result. A caller that gives up waiting has
// learned something about its own patience, not about the work, which is why
// the two facts never collapse into one.
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

// Await waits for the work to finish, or for ctx to end, and returns its
// Result with a second value saying whether it succeeded:
//
//   - work succeeded, wait completed — (Result, nil)
//   - work failed, wait completed — (Result carrying the failure, an error
//     wrapping it), so `if err != nil` catches a failure without the caller
//     having to look inside the Result first
//   - ctx ended first — (the zero Result, ctx's cause), and **the work still
//     runs** and still resolves the Future for anyone else watching
//
// The last is the property that makes a Future droppable: nothing is abandoned,
// so nothing needs cleaning up by whoever let go. It is also the one case
// where the two values disagree, and deliberately so — on a timeout `err` is
// non-nil and the zero Result reports `Ok()`, because the Result is not to be
// read when the wait did not complete. A caller that wants the work's fate
// waits for it, or watches Done.
func (f *Future[R]) Await(ctx context.Context) (Result[R], error) {
	if f == nil {
		return Result[R]{}, nil
	}

	if ctx == nil {
		return Result[R]{}, ErrNilContext
	}

	select {
	case <-f.done:
		if f.res.Err != nil {
			return f.res, fmt.Errorf("traits: work failed: %w", f.res.Err)
		}

		return f.res, nil
	case <-ctx.Done():
		// Not wrapped: a context cause is already a typed, comparable error,
		// and errors.Is against context.Canceled or DeadlineExceeded is a
		// caller's first move with it.
		return Result[R]{}, context.Cause(ctx)
	}
}

// ErrInvalidConfig is the shared cause for a rejected option. Packages alias
// it — `ownership.ErrInvalidConfig`, `dedupe.ErrInvalidConfig` — so a caller
// writes `errors.Is(err, <their package>.ErrInvalidConfig)` and gets the same
// value either way, which is what lets one error type serve three packages
// without a per-package wrapper to undo it.
var ErrInvalidConfig = errors.New("invalid configuration")

// ConfigError reports a rejected option. One definition serves every package
// whose options are named, so "which option was refused" is answered the same
// way everywhere.
//
// It is deliberately not the same type as TraitError, which describes a
// composition failure rather than an option: they have different fields and
// different readers, and merging them would mean a type with an Index that is
// always -1 in one use and the trait position in the other.
type ConfigError struct {
	Option string
	Reason error
}

func (e *ConfigError) Error() string {
	return fmt.Sprintf("option %q: %v", e.Option, e.Reason)
}

func (e *ConfigError) Unwrap() []error { return []error{ErrInvalidConfig, e.Reason} }
