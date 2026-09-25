package ownership

import (
	"context"
	"errors"
	"runtime/debug"

	"github.com/apsis-io/velocity/traits"
)

// MutateAsync is Mutate with the work moved to its own goroutine, and with
// **admission queued rather than refused**: the write borrow is waited for, and
// the Future reports the outcome once the callback has run.
//
//	mutation := owner.MutateAsync(ctx, update)
//	// ... elsewhere, whenever:
//	result, err := mutation.Await(ctx)
//
// ctx bounds the wait for the borrow, so a caller that gives up cancels the
// mutation before it starts rather than leaving it queued. It cannot cancel a
// callback already running — a callback has no context — so a Future dropped
// mid-callback is still completed and still released.
//
// **Mutate itself is unchanged and still refuses on conflict.** That is the
// important half: if the synchronous path queued too, every borrow would wait,
// and the no-wait invariant this package is built on would go with it. The choice
// is the caller's — `Mutate` to be told immediately, `MutateAsync` to wait for a
// turn.
//
// **Waiting here is a broadcast, not a queue.** The cell wakes every waiter when
// a borrow ends and the losers re-read the state, so a waiter holds nothing and
// cannot block a release. An ordered waiter list would be a queue the cell
// owns, and a re-entrant caller would queue behind itself with nothing timing
// it out — the deadlock this shape exists to avoid.
//
// **Re-entering from this callback reports `ErrConflict`, except in one shape.**
// `View`, `Mutate` and `BorrowMut` called from inside the callback are turned
// away at once, exactly as they would be from any other goroutine — the
// callback holds the write borrow, and saying so is more useful than waiting.
// That was worth checking rather than asserting: an earlier version of this
// comment said re-entering hangs, and it does not.
//
// The one shape that waits is a **nested `MutateAsync` whose Future the callback
// awaits**. The nested mutation queues, and the callback blocks on it holding
// the borrow the nested one needs, so the two wait on each other. This is the
// caller choosing to wait inside a critical section it holds — the same mistake
// as waiting on a `WaitGroup` you are inside — not a failure to detect
// anything. A context with a deadline turns it into an error at the point of the
// wait, which is why `ctx` bounds the admission wait and not only the caller's
// patience.
func (o *Owner[T]) MutateAsync[R any](ctx context.Context, fn func(*T) (R, error)) *traits.Future[R] {
	f := traits.NewFuture[R]()

	switch {
	case fn == nil:
		f.Complete(zero[R](), &ProjectionError{Operation: OpUpdate})
		return f
	case o == nil || o.c == nil:
		f.Complete(zero[R](), &ReleasedError{Operation: OpBorrowMut})
		return f
	case ctx == nil:
		// A goroutine panicking on a nil ctx would take the process with it, so
		// one is refused here rather than dereferenced there.
		f.Complete(zero[R](), traits.ErrNilContext)
		return f
	}

	go o.mutateAsync(ctx, f, fn)

	return f
}

// mutateAsync waits for the write borrow, then runs fn, and resolves f either
// way. The loop is the whole of the queue: try to be admitted, and on a
// conflict wait for the cell to say something moved.
func (o *Owner[T]) mutateAsync[R any](ctx context.Context, f *traits.Future[R], fn func(*T) (R, error)) {
	c := o.c

	for {
		if err := ctx.Err(); err != nil {
			f.Complete(zero[R](), context.Cause(ctx))
			return
		}

		c.mu.Lock()

		admitErr := c.admitWriteLocked(&o.h, modeUnique)
		changed := c.waitForChange()

		c.mu.Unlock()

		if admitErr != nil {
			// Only a CONFLICT is worth waiting out — being turned away is what a
			// queue is, and something else will change the answer. A terminal
			// refusal (released, moved, sealed) is different: no future change
			// can make this cell admit, so parking on the broadcast would sleep
			// until the context ended and then report the context's cause —
			// a different failure from the one that actually happened.
			if !errors.Is(admitErr, ErrConflict) {
				f.Complete(zero[R](), admitErr)
				return
			}

			select {
			case <-changed:
			case <-ctx.Done():
				f.Complete(zero[R](), context.Cause(ctx))
				return
			}

			continue
		}

		var (
			r        R
			err      error
			panicked any
			returned bool
		)

		func() {
			defer func() {
				if v := recover(); v != nil {
					panicked = v
				}

				// Release first and unconditionally, waking anything queued
				// behind this borrow before anything else can fail.
				c.mu.Lock()
				c.endWriteLocked(&o.h)
				c.mu.Unlock()

				switch {
				case panicked != nil:
					f.Complete(zero[R](), &traits.Panic{Value: panicked, Stack: debug.Stack()})
				case !returned:
					// runtime.Goexit. The release above ran, so the cell is
					// fine, but nothing came back — and reporting the zero
					// value would be a success the work never had.
					f.Complete(zero[R](), traits.ErrCallbackExit)
				default:
					f.Complete(r, err)
				}
			}()

			// The writer flag excludes every other access until the deferred end
			// above, so the address is exclusive for exactly that long.
			r, err = fn(&c.value)
			returned = true
		}()

		return
	}
}

func zero[R any]() R {
	var zero R
	return zero
}
