package async

import "context"

// Limit makes bounded versus unbounded execution explicit.
type Limit struct {
	value      int
	configured bool
	unlimited  bool
}

// Limited returns a bounded concurrency limit. New reports non-positive
// values as ErrInvalidLimit.
func Limited(n int) Limit { return Limit{value: n, configured: true} }

// Unlimited explicitly permits one goroutine per task.
var Unlimited = Limit{configured: true, unlimited: true}

func (l Limit) valid() error {
	if !l.configured || (!l.unlimited && l.value <= 0) {
		return &PlanError{Index: -1, Cause: ErrInvalidLimit}
	}
	return nil
}

// workers is how many goroutines the limit allows for n units of work.
func (l Limit) workers(n int) int {
	if l.unlimited {
		return n
	}
	return min(l.value, n)
}

// Task is one labeled operation for Gather, Race, or FirstSuccess.
type Task[T any] struct {
	Label string
	Run   func(context.Context) (T, error)
}

// Named builds a labeled Task without the struct literal.
func Named[T any](label string, run func(context.Context) (T, error)) Task[T] {
	return Task[T]{Label: label, Run: run}
}

// tasks wraps bare functions as unlabeled Tasks, for the *Funcs forms.
func tasks[T any](fns []func(context.Context) (T, error)) []Task[T] {
	out := make([]Task[T], len(fns))
	for i, fn := range fns {
		out[i].Run = fn
	}
	return out
}

// Runner is a concurrency policy — a Limit and optional Hooks — stated once
// and applied to every operation run through it.
//
//	run, err := async.New(async.Limited(8))
//	results, err := run.Map(ctx, items, process)
//	outcomes, err := run.Gather(ctx, fetchA, fetchB)
//
// A Runner is immutable once built and safe to share between goroutines.
type Runner struct {
	limit Limit
	hooks Hooks
}

// Option configures a Runner and is sealed to this package.
type Option interface {
	apply(*Runner) error
}

type optionFunc func(*Runner) error

func (f optionFunc) apply(r *Runner) error { return f(r) }

// WithHooks installs instrumentation callbacks. Nil callbacks are skipped.
func WithHooks(hooks Hooks) Option {
	return optionFunc(func(r *Runner) error {
		r.hooks = hooks
		return nil
	})
}

// New validates the limit — bounded or unbounded is a decision, not a
// default, so an unset Limit is ErrInvalidLimit — and applies the options.
func New(limit Limit, opts ...Option) (*Runner, error) {
	if err := limit.valid(); err != nil {
		return nil, err
	}
	r := &Runner{limit: limit}
	for _, opt := range opts {
		if opt == nil {
			return nil, &PlanError{Index: -1, Cause: ErrNilOption}
		}
		if err := opt.apply(r); err != nil {
			return nil, err
		}
	}
	return r, nil
}

// Must is New for a fixed argument list that cannot fail, in the manner of
// regexp.MustCompile: it panics on error, for package-level and constructor
// use where there is nowhere to return one.
func Must(r *Runner, err error) *Runner {
	if err != nil {
		panic(err)
	}
	return r
}

// Limit reports the configured concurrency limit.
func (r *Runner) Limit() Limit { return r.limit }

func (r *Runner) validTasks(n int, run func(int) bool) error {
	if r == nil {
		return &PlanError{Index: -1, Cause: ErrNilRunner}
	}
	if n == 0 {
		return &PlanError{Index: -1, Cause: ErrNoTasks}
	}
	for i := range n {
		if !run(i) {
			return &PlanError{Index: i, Cause: ErrNilTask}
		}
	}
	return nil
}

func validTasks[T any](r *Runner, tasks []Task[T]) error {
	return r.validTasks(len(tasks), func(i int) bool { return tasks[i].Run != nil })
}
