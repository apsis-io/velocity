package async

import (
	"context"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"time"
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
		return nil, &PlanError{Index: -1, Cause: ErrNilRunner}
	}
	if fn == nil {
		return nil, &PlanError{Index: -1, Cause: ErrNilTask}
	}
	results := make([]R, len(items))
	if len(items) == 0 {
		return results, nil
	}

	start := time.Now()
	done := ctx.Done()
	hook := r.hooks.OnTaskComplete
	var next atomic.Int64
	var failures itemErrors
	var wg sync.WaitGroup
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
				var err error
				if hook == nil {
					// Per-item clock reads are most of the dispatch cost, so
					// they are paid only when someone is listening.
					results[i], err = fn(ctx, items[i])
				} else {
					waited := time.Since(start)
					runStart := time.Now()
					results[i], err = fn(ctx, items[i])
					hook(i, "", waited, time.Since(runStart), err)
				}
				if err != nil {
					var zero R
					results[i] = zero
					failures.add(i, err)
				}
			}
		})
	}
	wg.Wait()

	// Every index below next was claimed and therefore ran; the counter can
	// overshoot by one per worker that raced past the end.
	if claimed := min(int(next.Load()), len(items)); claimed < len(items) {
		cause := context.Cause(ctx)
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

// ForEach is Map for a function that produces only an error. The returned
// error is the same join of *ItemError values.
func (r *Runner) ForEach[T any](ctx context.Context, items []T, fn func(context.Context, T) error) error {
	if fn == nil {
		return &PlanError{Index: -1, Cause: ErrNilTask}
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
