package async

import (
	"context"

	"github.com/apsis-io/velocity/ownership"
)

// Broadcast runs read-only workers concurrently against one owned input.
// Each worker receives a shallow value copy under its own read borrow, which
// Owner.View already permits concurrently.
func (r *Runner) Broadcast[T, R any](ctx context.Context, input *ownership.Owner[T], workers ...func(context.Context, T) (R, error)) ([]Outcome[R], error) {
	if input == nil {
		return nil, &PlanError{Index: -1, Cause: ErrNilOwner}
	}
	if err := r.validTasks(len(workers), func(i int) bool { return workers[i] != nil }); err != nil {
		return nil, err
	}
	tasks := make([]Task[R], len(workers))
	for i, worker := range workers {
		tasks[i] = Task[R]{Run: func(taskCtx context.Context) (R, error) {
			return input.View(func(value T) (R, error) { return worker(taskCtx, value) })
		}}
	}
	return r.Gather(ctx, tasks...)
}
