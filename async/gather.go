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

// Gather executes every task and returns outcomes in source-index order,
// with every error joined. Take and Last are recipes over the returned
// slice, not separate operations.
func (r *Runner) Gather[T any](ctx context.Context, tasks ...Task[T]) ([]Outcome[T], error) {
	if err := validTasks(r, tasks); err != nil {
		return nil, err
	}
	// Unlike race, Gather never cancels early: wg.Wait below guarantees every
	// task has returned before this does, so a derived cancellable context
	// would only ever be canceled by its own defer. Passing ctx through keeps
	// parent cancellation working and avoids allocating one per call.
	outcomes := make([]Outcome[T], len(tasks))

	var (
		wg      sync.WaitGroup
		permits chan struct{}
	)
	if !r.limit.unlimited {
		permits = make(chan struct{}, r.limit.value)
	}
	// The permit is taken here rather than inside the task goroutine so that a
	// Limit bounds goroutines, not just running work. Acquiring inside would
	// spawn one goroutine per task up front and park all but limit of them,
	// which costs a stack each and applies no backpressure to the caller; a
	// plan over a large collection would then hold thousands of parked
	// goroutines. Blocking the submitting goroutine is the backpressure.
	for i, task := range tasks {
		var waited time.Duration

		if permits != nil {
			waitStart := time.Now()

			select {
			case permits <- struct{}{}:
				waited = time.Since(waitStart)
			case <-ctx.Done():
				// Neither this task nor any after it will start.
				r.cancelRemaining(tasks, outcomes, i, time.Since(waitStart), context.Cause(ctx))
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
			if hook := r.hooks.OnTaskComplete; hook != nil {
				hook(i, task.Label, waited, duration, err)
			}
		})
	}

	wg.Wait()

	return outcomes, joinedErrors(outcomes)
}

// cancelRemaining records tasks from first onward as never started. Only the
// task at first actually queued for a permit, so the rest report no wait.
func (r *Runner) cancelRemaining[T any](tasks []Task[T], outcomes []Outcome[T], first int, waited time.Duration, err error) {
	for i := first; i < len(tasks); i++ {
		label := tasks[i].Label

		outcomes[i] = Outcome[T]{Index: i, Label: label, Err: err}
		if hook := r.hooks.OnTaskComplete; hook != nil {
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

// GatherFuncs is Gather for unlabeled functions, which is most calls.
func (r *Runner) GatherFuncs[T any](ctx context.Context, fns ...func(context.Context) (T, error)) ([]Outcome[T], error) {
	return r.Gather(ctx, tasks(fns)...)
}

// ForEachFuncs is GatherFuncs for functions that produce only an error, and is
// to GatherFuncs what ForEach is to Map. The error-only form is the common one:
// most work either has no result worth collecting, or collects it somewhere
// else — into a store, onto a channel, into an owned value the caller already
// holds. Without it, such a caller writes func(context.Context) (struct{},
// error) and discards a slice of outcomes it never wanted, or routes through
// Map over a single-item collection, which is a worse version of the same idea.
//
// The returned error is the same join GatherFuncs returns: every function's own
// error, in submission order, or nil if none failed. It is not a join of
// *ItemError values — those come from Map and ForEach, which have an index to
// report, where a function list does not. So Failures does not apply here; a
// caller that needs to know which function failed is already holding them in a
// slice it wrote.
func (r *Runner) ForEachFuncs(ctx context.Context, fns ...func(context.Context) error) error {
	_, err := r.Gather(ctx, funcsToOutcomes(fns)...)
	return err
}

// funcsToOutcomes adapts the error-only functions to Gather's shape with a zero
// value, rather than teaching Gather two signatures. The zero R is never
// observed: ForEachFuncs drops the outcomes it did not come for.
func funcsToOutcomes(fns []func(context.Context) error) []Task[struct{}] {
	out := make([]Task[struct{}], len(fns))
	for i, fn := range fns {
		out[i].Run = func(ctx context.Context) (struct{}, error) { return struct{}{}, fn(ctx) }
	}

	return out
}

// RaceFuncs is Race for unlabeled functions.
func (r *Runner) RaceFuncs[T any](ctx context.Context, fns ...func(context.Context) (T, error)) (Outcome[T], error) {
	return r.Race(ctx, tasks(fns)...)
}

// FirstSuccessFuncs is FirstSuccess for unlabeled functions.
func (r *Runner) FirstSuccessFuncs[T any](ctx context.Context, fns ...func(context.Context) (T, error)) (Outcome[T], error) {
	return r.FirstSuccess(ctx, tasks(fns)...)
}

func race[T any](ctx context.Context, r *Runner, tasks []Task[T], successOnly bool) (Outcome[T], error) {
	if err := validTasks(r, tasks); err != nil {
		return Outcome[T]{}, err
	}

	// A race with no contenders has no winner, and the zero Outcome says the
	// zero value won. Unlike Gather, which returns empty and nil for an empty
	// set, this has to say the task set was empty.
	if len(tasks) == 0 {
		return Outcome[T]{}, &TaskError{Index: -1, Cause: ErrNoTasks}
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	completions := make(chan Outcome[T], len(tasks))

	var (
		wg      sync.WaitGroup
		permits chan struct{}
	)
	if !r.limit.unlimited {
		permits = make(chan struct{}, r.limit.value)
	}

	for i, task := range tasks {
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

					if hook := r.hooks.OnTaskComplete; hook != nil {
						hook(i, task.Label, time.Since(waitStart), 0, err)
					}

					return
				}
			}

			runStart := time.Now()
			value, err := task.Run(ctx)

			outcome := Outcome[T]{Index: i, Label: task.Label, Value: value, Err: err}
			completions <- outcome

			if hook := r.hooks.OnTaskComplete; hook != nil {
				hook(i, task.Label, waited, time.Since(runStart), err)
			}
		})
	}

	var errs []error

	for range tasks {
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
func (r *Runner) Race[T any](ctx context.Context, tasks ...Task[T]) (Outcome[T], error) {
	return race(ctx, r, tasks, false)
}

// FirstSuccess returns the first successful outcome and cancels siblings. If
// every task fails, it returns all task errors joined in completion order.
// Non-cooperative siblings may continue running after a success.
func (r *Runner) FirstSuccess[T any](ctx context.Context, tasks ...Task[T]) (Outcome[T], error) {
	return race(ctx, r, tasks, true)
}
