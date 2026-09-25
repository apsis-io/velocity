package ownership

import (
	"context"
	"fmt"
	"runtime/debug"

	"github.com/apsis-io/velocity/traits"
)

// Panic captures a callback panic on a mutation that ran on its own goroutine.
// The same value is reported for Mutate and MutateAsync, so a caller that
// recovers the synchronous case can check the asynchronous one.
//
// It lives here rather than in traits because what it describes is a callback
// in this package's contract. The handle that carries it is shared.
type Panic struct {
	Value any
	Stack []byte
}

func (p *Panic) Error() string {
	return fmt.Sprintf("ownership: panic in mutation callback: %v\n%s", p.Value, p.Stack)
}

func (p *Panic) Unwrap() error {
	if err, ok := p.Value.(error); ok {
		return err
	}

	return nil
}

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
// **Re-entering the cell from this callback hangs.** A callback that reaches the
// same Owner, or any other borrower of the cell, waits for a borrow that only
// its own return can release. With `Mutate` that is an immediate
// `ErrConflict`; here it is a wait that never ends. This is the cost of
// queueing, and the same cost `sync.RWMutex` and this package's own
// `async.Mutex` carry. It is stated here because a method that returns
// immediately invites a caller not to check.
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
			// Not an error to report: being turned away is what a queue is. A
			// cell that has been sealed or moved will never admit, so its change
			// is the only signal a waiter gets — the loop re-reads the state and
			// `admitWriteLocked` reports the terminal condition itself.
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

				if panicked != nil {
					f.Complete(zero[R](), &Panic{Value: panicked, Stack: debug.Stack()})
					return
				}

				f.Complete(r, err)
			}()

			// The writer flag excludes every other access until the deferred end
			// above, so the address is exclusive for exactly that long.
			r, err = fn(&c.value)
		}()

		return
	}
}

func zero[R any]() R {
	var zero R
	return zero
}
