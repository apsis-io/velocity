package async

import "context"

// Limit makes bounded versus unbounded execution explicit.
type Limit struct {
	value      int
	configured bool
	unlimited  bool
}

// Limited returns a bounded concurrency limit. NewPlan reports non-positive
// values as ErrInvalidLimit.
func Limited(n int) Limit { return Limit{value: n, configured: true} }

// Unlimited explicitly permits one goroutine per task.
var Unlimited = Limit{configured: true, unlimited: true}

// Task is one labeled operation in a Plan.
type Task[T any] struct {
	Label string
	Run   func(context.Context) (T, error)
}

// Plan is an immutable task collection and concurrency policy.
type Plan[T any] struct {
	tasks []Task[T]
	limit Limit
	hooks Hooks
}

// NewPlan validates and copies tasks so later caller mutations cannot change
// execution.
func NewPlan[T any](limit Limit, hooks Hooks, tasks ...Task[T]) (Plan[T], error) {
	plan := Plan[T]{tasks: append([]Task[T](nil), tasks...), limit: limit, hooks: hooks}
	if err := plan.valid(); err != nil {
		return Plan[T]{}, err
	}
	return plan, nil
}

func (p Plan[T]) valid() error {
	if !p.limit.configured || (!p.limit.unlimited && p.limit.value <= 0) {
		return &PlanError{Index: -1, Cause: ErrInvalidLimit}
	}
	if len(p.tasks) == 0 {
		return &PlanError{Index: -1, Cause: ErrNoTasks}
	}
	for i, task := range p.tasks {
		if task.Run == nil {
			return &PlanError{Index: i, Cause: ErrNilTask}
		}
	}
	return nil
}
