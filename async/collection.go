package async

import (
	"context"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/apsis-io/velocity/traits"
)

// Map applies fn to every item concurrently and returns the results in input
// order. It is the homogeneous counterpart of Gather: where a Plan holds
// distinct labeled tasks, Map runs one function over a collection, so it
// dispatches from a fixed pool of Limit goroutines rather than spawning one
// per item, and returns a bare slice rather than an Outcome per item.
//
// Failures are the exception, so they are reported out of band: the returned
// error joins one *ItemError per failed item, carrying its index, and the
// result slot for a failed item is the zero R whatever fn returned beside
// its error. A caller who needs to know which items failed walks the joined
// error with errors.As; one who does not can treat it as a single error.
//
// An empty collection returns an empty slice and no error, unlike an empty
// Plan, since a collection is a value rather than a configuration.
//
// Hooks.OnTaskComplete receives the item's queueing delay as waited: the time
// between the call and a worker picking the item up. That is not a permit wait,
// because no permits exist in a pool, but it answers the same question of how
// long the item sat before its own work began.
//
// Cancellation stops workers from taking further items; the one each is
// running finishes. Items never picked up report context.Cause(ctx) with
// waited set to the delay at which they were abandoned and duration zero.
// An item that is never picked up never runs fn, so per-item cleanup
// registered before the call and undone inside fn is left undone for it.
// Hooks.OnTaskComplete fires for every item including those, with the
// cancellation cause, and is the place for such cleanup; or sweep every
// item afterwards, if the cleanup is idempotent.
//
// To fan out over an owned slice, run Map inside the read: workers finish
// before Map returns, so the borrow covers them all and its value never
// escapes the callback.
//
//	results, err := owner.View(func(items []Item) ([]Result, error) {
//	    return run.Map(ctx, items, process)
//	})
func (r *Runner) Map[T, R any](ctx context.Context, items []T, fn func(context.Context, T) (R, error)) ([]R, error) {
	if r == nil {
		return nil, &TaskError{Index: -1, Cause: ErrNilReceiver}
	}

	if fn == nil {
		return nil, &TaskError{Index: -1, Cause: ErrNilTask}
	}

	results := make([]R, len(items))
	if len(items) == 0 {
		return results, nil
	}

	start := time.Now()
	done := ctx.Done()
	hook := r.hooks.OnTaskComplete

	var (
		next     atomic.Int64
		failures itemErrors
		wg       sync.WaitGroup
	)
	for range r.limit.workers(len(items)) {
		wg.Go(func() {
			for {
				// A nil done (context.Background) never selects, so falls
				// through to claiming; this avoids ctx.Err's mutex per item.
				select {
				case <-done:
					return
				default:
				}

				i := int(next.Add(1) - 1)
				if i >= len(items) {
					return
				}

				r.mapItem(ctx, results, &failures, i, items[i], fn, start, hook)
			}
		})
	}

	wg.Wait()

	// Every index below next was claimed and therefore ran; the counter can
	// overshoot by one per worker that raced past the end.
	if claimed := min(int(next.Load()), len(items)); claimed < len(items) {
		// Two things strand items, and they are not the same answer. A
		// cancelled context has a cause worth reporting. A worker that stopped
		// does not — and context.Cause is nil for a context nobody cancelled,
		// so taking it unconditionally files an ItemError whose Err field is
		// nil: a non-nil error that wraps nothing, where errors.Is, errors.As
		// and the log message all say nothing while `if err != nil` insists
		// something failed. ErrWorkerExit rather than ErrCallbackExit, because
		// these items never ran: no callback was entered, let alone exited.
		cause := context.Cause(ctx)
		if cause == nil {
			cause = ErrWorkerExit
		}

		waited := time.Since(start)

		for i := claimed; i < len(items); i++ {
			failures.add(i, cause)

			if hook != nil {
				hook(i, "", waited, 0, cause)
			}
		}
	}

	return results, failures.join()
}

// mapItem runs fn for one item and records the outcome.
//
// It is a method rather than a block so that each item gets its own defer:
// a defer inside the dispatch loop would run once per *worker* rather than
// once per item, and a runtime.Goexit takes the whole worker with it.
//
// The recording happens in that defer, so an item whose function ends without
// returning is recorded as ErrCallbackExit rather than left as a zero result
// with no error beside it. The defer does not call recover, so a panicking
// item still takes the process down — deliberate, and the same rule Gather
// follows.
func (r *Runner) mapItem[T, R any](
	ctx context.Context,
	results []R,
	failures *itemErrors,
	i int,
	item T,
	fn func(context.Context, T) (R, error),
	start time.Time,
	hook func(int, string, time.Duration, time.Duration, error),
) {
	var (
		value    R
		err      error
		waited   time.Duration
		duration time.Duration
		returned bool
	)

	defer func() {
		if !returned {
			var zero R

			value, err = zero, traits.ErrCallbackExit
		}

		results[i] = value

		if err != nil {
			var zero R

			results[i] = zero
			failures.add(i, err)
		}

		if hook != nil {
			hook(i, "", waited, duration, err)
		}
	}()

	if hook == nil {
		// Per-item clock reads are most of the dispatch cost, so they are paid
		// only when someone is listening.
		value, err = fn(ctx, item)
		returned = true

		return
	}

	waited = time.Since(start)
	runStart := time.Now()
	value, err = fn(ctx, item)
	duration = time.Since(runStart)
	returned = true
}

// ForEach is Map for a function that produces only an error. The returned
// error is the same join of *ItemError values.
func (r *Runner) ForEach[T any](ctx context.Context, items []T, fn func(context.Context, T) error) error {
	if fn == nil {
		return &TaskError{Index: -1, Cause: ErrNilTask}
	}

	_, err := r.Map(ctx, items, func(ctx context.Context, item T) (struct{}, error) {
		return struct{}{}, fn(ctx, item)
	})

	return err
}

// itemErrors collects failures from workers. Failures are rare, so the
// success path touches only the atomic counter and the result slot.
type itemErrors struct {
	mu   sync.Mutex
	errs []error
}

func (e *itemErrors) add(index int, err error) {
	e.mu.Lock()
	e.errs = append(e.errs, &ItemError{Index: index, Err: err})
	e.mu.Unlock()
}

// join returns the failures in index order, so the error reads the same
// regardless of which worker finished first.
func (e *itemErrors) join() error {
	if len(e.errs) == 0 {
		return nil
	}

	slices.SortFunc(e.errs, func(a, b error) int {
		return a.(*ItemError).Index - b.(*ItemError).Index
	})

	return errors.Join(e.errs...)
}
