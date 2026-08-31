package async

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Outcome identifies one source task and its terminal result.
type Outcome[T any] struct {
	Index int
	Label string
	Value T
	Err   error
}

func execute[T any](ctx context.Context, plan Plan[T]) ([]Outcome[T], error) {
	if err := plan.valid(); err != nil {
		return nil, err
	}
	// Unlike race, Gather never cancels early: wg.Wait below guarantees every
	// task has returned before this does, so a derived cancellable context
	// would only ever be canceled by its own defer. Passing ctx through keeps
	// parent cancellation working and avoids allocating one per call.
	outcomes := make([]Outcome[T], len(plan.tasks))
	var wg sync.WaitGroup
	var permits chan struct{}
	if !plan.limit.unlimited {
		permits = make(chan struct{}, plan.limit.value)
	}
	// The permit is taken here rather than inside the task goroutine so that a
	// Limit bounds goroutines, not just running work. Acquiring inside would
	// spawn one goroutine per task up front and park all but limit of them,
	// which costs a stack each and applies no backpressure to the caller; a
	// plan over a large collection would then hold thousands of parked
	// goroutines. Blocking the submitting goroutine is the backpressure.
	for i, task := range plan.tasks {
		var waited time.Duration
		if permits != nil {
			waitStart := time.Now()
			select {
			case permits <- struct{}{}:
				waited = time.Since(waitStart)
			case <-ctx.Done():
				// Neither this task nor any after it will start.
				cancelRemaining(plan, outcomes, i, time.Since(waitStart), context.Cause(ctx))
				wg.Wait()
				return outcomes, joinedErrors(outcomes)
			}
		}
		wg.Go(func() {
			if permits != nil {
				defer func() { <-permits }()
			}
			runStart := time.Now()
			value, err := task.Run(ctx)
			duration := time.Since(runStart)
			outcomes[i] = Outcome[T]{Index: i, Label: task.Label, Value: value, Err: err}
			if hook := plan.hooks.OnTaskComplete; hook != nil {
				hook(i, task.Label, waited, duration, err)
			}
		})
	}
	wg.Wait()
	return outcomes, joinedErrors(outcomes)
}

// cancelRemaining records tasks from first onward as never started. Only the
// task at first actually queued for a permit, so the rest report no wait.
func cancelRemaining[T any](plan Plan[T], outcomes []Outcome[T], first int, waited time.Duration, err error) {
	for i := first; i < len(plan.tasks); i++ {
		label := plan.tasks[i].Label
		outcomes[i] = Outcome[T]{Index: i, Label: label, Err: err}
		if hook := plan.hooks.OnTaskComplete; hook != nil {
			if i > first {
				waited = 0
			}
			hook(i, label, waited, 0, err)
		}
	}
}

func joinedErrors[T any](outcomes []Outcome[T]) error {
	failed := 0
	for _, outcome := range outcomes {
		if outcome.Err != nil {
			failed++
		}
	}
	if failed == 0 {
		return nil
	}
	errs := make([]error, 0, failed)
	for _, outcome := range outcomes {
		if outcome.Err != nil {
			errs = append(errs, outcome.Err)
		}
	}
	return errors.Join(errs...)
}

// Gather executes every task and returns outcomes in source-index order.
// Take and Last are recipes over the returned slice, not separate APIs.
func Gather[T any](ctx context.Context, plan Plan[T]) ([]Outcome[T], error) {
	return execute(ctx, plan)
}

func race[T any](ctx context.Context, plan Plan[T], successOnly bool) (Outcome[T], error) {
	if err := plan.valid(); err != nil {
		return Outcome[T]{}, err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	completions := make(chan Outcome[T], len(plan.tasks))
	var wg sync.WaitGroup
	var permits chan struct{}
	if !plan.limit.unlimited {
		permits = make(chan struct{}, plan.limit.value)
	}
	for i, task := range plan.tasks {
		i, task := i, task
		wg.Go(func() {
			var waited time.Duration
			if permits != nil {
				waitStart := time.Now()
				select {
				case permits <- struct{}{}:
					waited = time.Since(waitStart)
					defer func() { <-permits }()
				case <-ctx.Done():
					err := context.Cause(ctx)
					outcome := Outcome[T]{Index: i, Label: task.Label, Err: err}
					completions <- outcome
					if hook := plan.hooks.OnTaskComplete; hook != nil {
						hook(i, task.Label, time.Since(waitStart), 0, err)
					}
					return
				}
			}
			runStart := time.Now()
			value, err := task.Run(ctx)
			outcome := Outcome[T]{Index: i, Label: task.Label, Value: value, Err: err}
			completions <- outcome
			if hook := plan.hooks.OnTaskComplete; hook != nil {
				hook(i, task.Label, waited, time.Since(runStart), err)
			}
		})
	}

	var errs []error
	for range plan.tasks {
		select {
		case outcome := <-completions:
			if !successOnly || outcome.Err == nil {
				cancel()
				return outcome, outcome.Err
			}
			errs = append(errs, outcome.Err)
		case <-ctx.Done():
			return Outcome[T]{}, context.Cause(ctx)
		}
	}
	wg.Wait()
	return Outcome[T]{}, errors.Join(errs...)
}

// Race returns the first completed outcome, whether it succeeded or failed,
// and cancels sibling task contexts. Non-cooperative siblings may continue
// running in the background after Race returns.
//
// Unlike Gather, a Limit here bounds running work but not goroutines: every
// task is spawned up front and parks on the permit. Returning on the first
// completion requires reaching the collector, which blocking the submitting
// goroutine on a permit would prevent. Prefer Gather when racing a large
// enough collection for one parked goroutine per task to matter.
func Race[T any](ctx context.Context, plan Plan[T]) (Outcome[T], error) {
	return race(ctx, plan, false)
}

// FirstSuccess returns the first successful outcome and cancels siblings. If
// every task fails, it returns all task errors joined in completion order.
// Non-cooperative siblings may continue running after a success.
func FirstSuccess[T any](ctx context.Context, plan Plan[T]) (Outcome[T], error) {
	return race(ctx, plan, true)
}
