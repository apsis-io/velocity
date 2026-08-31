package async

import (
	"context"

	"github.com/apsis-io/velocity/ownership"
)

// Broadcast runs read-only workers concurrently against one owned input.
// Each worker receives a shallow value copy from an independent read borrow.
func Broadcast[T, R any](ctx context.Context, input *ownership.Owner[T], limit Limit, hooks Hooks, workers ...func(context.Context, T) (R, error)) ([]Outcome[R], error) {
	if input == nil {
		return nil, &PlanError{Index: -1, Cause: ErrNilOwner}
	}
	tasks := make([]Task[R], len(workers))
	for i, worker := range workers {
		worker := worker
		if worker == nil {
			continue
		}
		tasks[i] = Task[R]{Run: func(taskCtx context.Context) (R, error) {
			return input.Read(func(access ownership.ReadAccess[T]) (R, error) {
				return access.Project(func(value T) (R, error) {
					return worker(taskCtx, value)
				})
			})
		}}
	}
	plan, err := NewPlan(limit, hooks, tasks...)
	if err != nil {
		return nil, err
	}
	return Gather(ctx, plan)
}
