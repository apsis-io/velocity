package ownership

import (
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

// MutateAsync is Mutate with the work moved to its own goroutine. The borrow is
// taken before MutateAsync returns, or refused before it returns with
// ErrConflict, exactly as Mutate does; what becomes asynchronous is the
// callback, and the Future reports its outcome.
//
//	mutation := owner.MutateAsync(update)
//	// ... elsewhere, whenever:
//	result, err := mutation.Await(ctx)
//
// **Admission stays synchronous, and that is the whole design.** The Future is
// about the callback's outcome, not about acquiring: a goroutine that *waited*
// for the borrow would put a wait inside the cell, and a cell with waiters is
// how a cycle gets one, which is what the rest of this package refuses to have.
// So a caller keeps the synchronous admission contract, and a failure arriving
// on the Future is a failure rather than a rejection.
//
// **The callback may panic here without taking the process with it**, which it
// would if it ran on a caller's goroutine. The borrow is released before the
// panic is converted, so a panicking callback cannot wedge the cell, and the
// Future reports a *Panic — the same information a caller recovering the
// synchronous form would have. This is the one place the package is more
// forgiving than its own rule that callbacks must not panic, and it is
// forgiving because there is no caller left to be forgiving towards.
func (o *Owner[T]) MutateAsync[R any](fn func(*T) (R, error)) *traits.Future[R] {
	f := traits.NewFuture[R]()

	if fn == nil {
		f.CompleteResult(traits.Result[R]{Err: &ProjectionError{Operation: OpUpdate}})
		return f
	}

	if o == nil || o.c == nil {
		f.CompleteResult(traits.Result[R]{Err: &ReleasedError{Operation: OpBorrowMut}})
		return f
	}

	// Admission is the cell's decision and it does not wait, so this either
	// takes the write borrow now or reports why it could not. Nothing below runs
	// in the second case and no goroutine is started.
	c := o.c

	c.mu.Lock()

	if err := c.admitWriteLocked(&o.h, modeUnique); err != nil {
		c.mu.Unlock()
		f.CompleteResult(traits.Result[R]{Err: err})

		return f
	}

	c.mu.Unlock()

	go func() {
		var (
			r        R
			err      error
			panicked any
		)

		defer func() {
			if v := recover(); v != nil {
				panicked = v
			}

			// Release first and unconditionally. The writer flag is the
			// exclusion, and it is cleared before anything else can fail —
			// including a panic above, which is the one failure that would
			// otherwise leave the cell wedged.
			c.mu.Lock()
			c.endWriteLocked(&o.h)
			c.mu.Unlock()

			if panicked != nil {
				var zero R
				f.Complete(zero, &Panic{Value: panicked, Stack: debug.Stack()})

				return
			}

			f.Complete(r, err)
		}()

		// The writer flag excludes every other access until the deferred end
		// above, so handing out the address is exclusive for exactly that long —
		// the same guarantee scopedMutate gives, across a return boundary.
		r, err = fn(&c.value)
	}()

	return f
}
