package async

import (
	"context"
	"time"
)

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
		return &TaskError{Index: -1, Cause: ErrInvalidLimit}
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
			return nil, &TaskError{Index: -1, Cause: ErrNilOption}
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

// validTasks rejects a malformed task. It does NOT reject an empty set: a
// fan-out over nothing is a fan-out that did nothing, and Gather, Map and
// ForEach all return an empty result and a nil error for it. Race and
// FirstSuccess are the exception, because a race with no contenders has no
// winner — they check for themselves, and a zero Outcome with a nil error
// would be a claim that the zero value won.
func (r *Runner) validTasks(n int, run func(int) bool) error {
	if r == nil {
		return &TaskError{Index: -1, Cause: ErrNilReceiver}
	}

	for i := range n {
		if !run(i) {
			return &TaskError{Index: i, Cause: ErrNilTask}
		}
	}

	return nil
}

func validTasks[T any](r *Runner, tasks []Task[T]) error {
	return r.validTasks(len(tasks), func(i int) bool { return tasks[i].Run != nil })
}

// acquire takes one permit, waiting no longer than ctx. It reports how long the
// wait was and whether a permit was taken; a false means ctx ended first, and
// the caller must not run anything.
//
// permits is nil for an Unlimited runner, which admits without waiting, so the
// unlimited case costs a nil check rather than a send.
//
// The clock is read only when a hook is installed to receive the answer, since
// the reads are otherwise cost with no reader. Map makes the same trade for the
// same reason, and this is the shared form of it: Gather and ErrGroup.GoContext
// both waited for a permit this way, and a third copy for a third caller is how
// two of them drift apart.
func (r *Runner) acquire(ctx context.Context, permits chan struct{}) (waited time.Duration, ok bool) {
	if permits == nil {
		return 0, true
	}

	var start time.Time
	if r.hooks.OnTaskComplete != nil {
		start = time.Now()
	}

	select {
	case permits <- struct{}{}:
		return since(start), true
	case <-ctx.Done():
		return since(start), false
	}
}

// since is the time from a start, or zero when the clock was never read because
// nothing was listening for the answer.
func since(start time.Time) time.Duration {
	if start.IsZero() {
		return 0
	}

	return time.Since(start)
}
