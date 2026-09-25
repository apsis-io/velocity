package async

import (
	"context"
	"runtime/debug"
	"time"

	"github.com/apsis-io/velocity/traits"
)

// Submit starts fn as one task and returns a handle on its outcome.
//
//	f := run.Submit(ctx, fetch)
//	// ... elsewhere, whenever:
//	result, err := f.Await(ctx)
//
// It is the Future-shaped form of what ErrGroup.GoContext does: a submission
// that reports whether it was accepted, and a hook that hears about the ones
// that were not. The difference is what comes back — a handle on the work
// rather than a bool about the submission — and the difference that follows
// from it is that Submit is asynchronous by contract. The caller has usually
// walked away by the time anything goes wrong.
//
// **Dropping the handle is safe.** The work runs and resolves it for anyone
// still watching, so a Future needs no cleanup, and giving up on Await says
// something about the caller's patience rather than about the work.
//
// **Submit is bounded at one, by being one task.** That is the same bound
// Gather's per-call permits give it, arrived at structurally rather than by
// counting: a Limit states how many of a single operation's tasks may run at
// once, and this operation has one. A Limit therefore applies to Submit the
// way it applies to Gather — it simply has nothing to do here. It does **not**
// bound the number of Submits a caller may make, because two concurrent calls
// each get the full limit rather than sharing one, and that is the Runner's
// property rather than this function's: Gather and race each build their own
// permits for the same reason.
//
// The consequence is one goroutine per call, so Submit is for a handful of
// tasks written out by hand — start these three things now, ask later — which
// is the shape GoContext exists for inside an ErrGroup. A collection wants Map
// or Gather, which dispatch from a fixed pool. Giving the Runner a shared pool
// so a limit means one thing across operations was considered and rejected: it
// changes what Limit *is*, and a Gather called from inside a task of another
// Gather would then deadlock on a permit the outer call is holding.
//
// **A Future always resolves, so a panic does not escape.** fn panicking
// becomes a *traits.Panic, and fn ending through runtime.Goexit becomes
// traits.ErrCallbackExit. This is where Submit differs from Gather, which lets
// a panicking task take the process with it, and the difference is the
// asynchrony rather than a preference: a Gather task panics at the call site,
// where the submitter and the stack are both present, while a Submit task
// panics later, in a goroutine the submitter has already left. Neither is a
// good time to take a process down, and the later one is worse. A handle that
// never resolves would be worse than either — every awaiter's timeout would be
// reported as the work's failure, which is the one thing Await promises not to
// do.
//
// The hooks see a submitted task with index -1 and an empty label, as Map
// reports an unlabeled item with one, because there is no collection for an
// index to come from and Submit takes no name to report.
//
// **Every Submit that passes validation reports exactly once, and one that
// does not pass reports nothing at all** — a nil function was never a task,
// which is the rule Gather's validator and ErrGroup's nil check already
// follow. Note what is *not* here, because it is easy to assume from the
// others: unlike Gather and GoContext, there is no path in this function where
// a valid submission is handed over and then refused, since a one-task
// operation has no permit to wait for. A handle from Submit always has
// something behind it.
func (r *Runner) Submit[T any](ctx context.Context, fn func(context.Context) (T, error)) *traits.Future[T] {
	var zero T

	f := traits.NewFuture[T]()

	switch {
	case r == nil:
		f.Complete(zero, &TaskError{Index: -1, Cause: ErrNilReceiver})

		return f
	case fn == nil:
		f.Complete(zero, &TaskError{Index: -1, Cause: ErrNilTask})

		return f
	case ctx == nil:
		// A goroutine panicking on a nil ctx.Done() would take the process with
		// it, in a goroutine whose submitter cannot see the stack.
		f.Complete(zero, &TaskError{Index: -1, Cause: ErrNilContext})

		return f
	}

	go func() {
		var (
			result   T
			err      error
			panicked *traits.Panic
			returned bool
			duration time.Duration
		)

		// One deferred function resolves the handle on every exit, including the
		// one that never comes back. runtime.Goexit runs deferred calls and then
		// ends the goroutine without returning here, so anything written after
		// fn would not run — and a handle that never resolves reports every
		// awaiter's timeout as the work's own failure.
		//
		// recover is called in the defer rather than around it, which is what
		// makes one function enough: a panic arrives here with a non-nil value,
		// a Goexit with a nil one, and a normal return with a nil one and
		// returned set. The stack is captured here so it is the stack of the
		// panic rather than of this frame.
		defer func() {
			if value := recover(); value != nil {
				panicked = &traits.Panic{Value: value, Stack: debug.Stack()}
			}

			switch {
			case panicked != nil:
				err = panicked
			case !returned:
				// The deferred release ran and nothing came back, so the zero
				// value would be a success the work never had.
				err = traits.ErrCallbackExit
			}

			// The hook fires before the handle resolves, so a resolved Future
			// means the task has been reported — the order Gather already
			// gives, where a returned slice means every hook has run. It is not
			// free: a hook that panics leaves the handle unresolved. That case
			// is excluded by the hook contract rather than by physics, exactly
			// as a Drop that panics is, and the difference matters only to a
			// reader deciding whether to audit this. It is the only ordering
			// under which a caller can await a Future and then read what the
			// hook recorded.
			if hook := r.hooks.OnTaskComplete; hook != nil {
				hook(-1, "", 0, duration, err)
			}

			f.Complete(result, err)
		}()

		start := time.Now()
		result, err = fn(ctx)
		duration = time.Since(start)
		returned = true
	}()

	return f
}
