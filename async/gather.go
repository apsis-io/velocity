package async

import (
	"context"
	"errors"
	"sync"
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
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	outcomes := make([]Outcome[T], len(plan.tasks))
	var wg sync.WaitGroup
	var permits chan struct{}
	if !plan.limit.unlimited {
		permits = make(chan struct{}, plan.limit.value)
	}
	for i, task := range plan.tasks {
		i, task := i, task
		wg.Go(func() {
			if permits != nil {
				select {
				case permits <- struct{}{}:
					defer func() { <-permits }()
				case <-ctx.Done():
					outcomes[i] = Outcome[T]{Index: i, Label: task.Label, Err: context.Cause(ctx)}
					return
				}
			}
			value, err := task.Run(ctx)
			outcomes[i] = Outcome[T]{Index: i, Label: task.Label, Value: value, Err: err}
		})
	}
	wg.Wait()
	return outcomes, joinedErrors(outcomes)
}

func joinedErrors[T any](outcomes []Outcome[T]) error {
	errs := make([]error, 0, len(outcomes))
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
			if permits != nil {
				select {
				case permits <- struct{}{}:
					defer func() { <-permits }()
				case <-ctx.Done():
					return
				}
			}
			value, err := task.Run(ctx)
			completions <- Outcome[T]{Index: i, Label: task.Label, Value: value, Err: err}
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
func Race[T any](ctx context.Context, plan Plan[T]) (Outcome[T], error) {
	return race(ctx, plan, false)
}

// FirstSuccess returns the first successful outcome and cancels siblings. If
// every task fails, it returns all task errors joined in completion order.
// Non-cooperative siblings may continue running after a success.
func FirstSuccess[T any](ctx context.Context, plan Plan[T]) (Outcome[T], error) {
	return race(ctx, plan, true)
}
