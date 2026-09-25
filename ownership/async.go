package ownership

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"sync"
)

// ErrPending is what Future.Result reports while a mutation is still running. It
// is an error rather than a bool so the result can go straight to errors.Is
// without unpacking anything.
var ErrPending = errors.New("ownership: mutation still running")

// Panic captures a callback panic on a mutation that ran on its own goroutine.
// The same value is reported for Mutate and MutateAsync, so a caller that
// recovers the synchronous case can check the asynchronous one.
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

// Future is the eventual outcome of a mutation submitted with MutateAsync:
// still running, or finished with a value or an error.
//
// It is a receipt rather than a promise. Admission has already happened by the
// time a caller holds one, so the Future resolves to a *failure* and never to
// ErrConflict — a rejection is reported by the call that would have admitted
// the mutation.
type Future[R any] struct {
	done chan struct{}
	once sync.Once

	// Written by the mutating goroutine before done is closed and read by
	// whoever observes the close. The happens-before comes from the close, so
	// nothing guards these and a Future nobody ever looks at still completes
	// and releases its borrow.
	r   R
	err error
}

func (f *Future[R]) complete(r R, err error) {
	f.once.Do(func() {
		f.r, f.err = r, err
		close(f.done)
	})
}

// Done returns a channel closed when the mutation has finished, one way or the
// other. A nil Future is already finished, so its channel is closed and every
// read reports a released handle.
func (f *Future[R]) Done() <-chan struct{} {
	if f == nil {
		return closedDone
	}

	return f.done
}

var closedDone = func() <-chan struct{} {
	ch := make(chan struct{})
	close(ch)

	return ch
}()

// Result returns the outcome if the mutation has finished, and ErrPending if it
// has not. It never blocks, so a caller polling in a loop can starve the
// goroutine it is polling for — use Await or select on Done when the answer
// matters.
func (f *Future[R]) Result() (R, error) {
	if f == nil {
		var zero R
		return zero, &ReleasedError{Operation: OpBorrowMut}
	}

	select {
	case <-f.done:
		return f.r, f.err
	default:
		var zero R
		return zero, ErrPending
	}
}

// Await waits for the mutation to finish, or for ctx to end, and returns ctx's
// cause if the context ends first. **Giving up on the wait does not give up on
// the work**: the mutation still completes and still releases its borrow, and
// anyone still watching the Future sees the real outcome. Nothing here is
// abandoned and nothing needs cleaning up by the caller, which is the property
// that lets a Future be dropped without thought.
func (f *Future[R]) Await(ctx context.Context) (R, error) {
	var zero R

	if f == nil {
		return zero, &ReleasedError{Operation: OpBorrowMut}
	}

	select {
	case <-f.done:
		return f.r, f.err
	case <-ctx.Done():
		return zero, context.Cause(ctx)
	}
}

// MutateAsync is Mutate with the work moved to its own goroutine. The borrow is
// taken before MutateAsync returns, or refused before it returns with
// ErrConflict, exactly as Mutate does; what becomes asynchronous is the
// callback, and the Future reports that.
//
//	mutation := owner.MutateAsync(update)
//	// ... elsewhere, whenever:
//	value, err := mutation.Await(ctx)
//
// **The admission stays synchronous, and that is the whole design.** The Future
// is about the callback's outcome, not about acquiring: a goroutine that waited
// for the borrow would put a wait inside the cell, and a cell with waiters is
// how a cycle gets one, which is what the rest of this package refuses to have.
// So a caller learns of a conflict at the call, as with Mutate, and an error
// arriving on the Future is a failure rather than a rejection.
//
// **The callback may panic here without taking the process with it**, which it
// would if it ran on a caller's goroutine. The borrow is released before the
// panic is converted, so a panicking callback cannot wedge the cell, and the
// Future reports a *Panic — the same information a caller recovering the
// synchronous form would have. This is the one place the package is more
// forgiving than its own rule that callbacks must not panic, and it is
// forgiving because there is no caller left to be forgiving towards.
func (o *Owner[T]) MutateAsync[R any](fn func(*T) (R, error)) *Future[R] {
	if fn == nil {
		return failedFuture[R](&ProjectionError{Operation: OpUpdate})
	}

	if o == nil || o.c == nil {
		return failedFuture[R](&ReleasedError{Operation: OpBorrowMut})
	}

	// Admission is the cell's decision and it does not wait, so this either
	// takes the write borrow now or reports why it could not. Nothing below
	// runs in the second case and no goroutine is started.
	c := o.c

	c.mu.Lock()

	if err := c.admitWriteLocked(&o.h, modeUnique); err != nil {
		c.mu.Unlock()
		return failedFuture[R](err)
	}

	c.mu.Unlock()

	f := &Future[R]{done: make(chan struct{})}

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
				f.complete(zero, &Panic{Value: panicked, Stack: debug.Stack()})

				return
			}

			f.complete(r, err)
		}()

		// The writer flag excludes every other access until the deferred end
		// above, so handing out the address is exclusive for exactly that long —
		// the same guarantee scopedMutate gives, across a return boundary.
		r, err = fn(&c.value)
	}()

	return f
}

// failedFuture returns a Future already resolved to err, for a submission that
// never started. It is already finished rather than pending, so a caller that
// ignores the handle and checks the borrow finds the cell untouched.
func failedFuture[R any](err error) *Future[R] {
	var zero R

	f := &Future[R]{done: make(chan struct{})}
	f.complete(zero, err)

	return f
}
