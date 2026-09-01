package async

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// Map applies fn to every item concurrently and returns one Outcome per item
// in input order, with every error joined. It is the homogeneous counterpart
// of Gather: where a Plan holds distinct labeled tasks, Map runs one function
// over a collection, so it dispatches from a fixed pool of Limit goroutines
// rather than spawning one per item. Outcome.Label is empty; Outcome.Index is
// the item's position.
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
//	results, err := owner.View(func(items []Item) ([]async.Outcome[Result], error) {
//	    return async.Map(ctx, async.Limited(8), async.Hooks{}, items, process)
//	})
func Map[T, R any](ctx context.Context, limit Limit, hooks Hooks, items []T, fn func(context.Context, T) (R, error)) ([]Outcome[R], error) {
	if err := limit.valid(); err != nil {
		return nil, err
	}
	if fn == nil {
		return nil, &PlanError{Index: -1, Cause: ErrNilTask}
	}
	outcomes := make([]Outcome[R], len(items))
	if len(items) == 0 {
		return outcomes, nil
	}

	start := time.Now()
	done := ctx.Done()
	hook := hooks.OnTaskComplete
	var next atomic.Int64
	var wg sync.WaitGroup
	for range limit.workers(len(items)) {
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
				if hook == nil {
					// Per-item clock reads are most of the dispatch cost, so
					// they are paid only when someone is listening.
					value, err := fn(ctx, items[i])
					outcomes[i] = Outcome[R]{Index: i, Value: value, Err: err}
					continue
				}
				waited := time.Since(start)
				runStart := time.Now()
				value, err := fn(ctx, items[i])
				duration := time.Since(runStart)
				outcomes[i] = Outcome[R]{Index: i, Value: value, Err: err}
				hook(i, "", waited, duration, err)
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
			outcomes[i] = Outcome[R]{Index: i, Err: cause}
			if hook != nil {
				hook(i, "", waited, 0, cause)
			}
		}
	}
	return outcomes, joinedErrors(outcomes)
}

// ForEach is Map for a function that produces only an error. It returns every
// item error joined; which items failed is recoverable through Map when it
// matters.
func ForEach[T any](ctx context.Context, limit Limit, hooks Hooks, items []T, fn func(context.Context, T) error) error {
	if fn == nil {
		return &PlanError{Index: -1, Cause: ErrNilTask}
	}
	_, err := Map(ctx, limit, hooks, items, func(ctx context.Context, item T) (struct{}, error) {
		return struct{}{}, fn(ctx, item)
	})
	return err
}
