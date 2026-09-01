// Package failsafeown runs failsafe-go policies over owned resources,
// releasing every result the policy chain produced but did not return.
//
// failsafe-go supplies the policies velocity's resilience package
// deliberately does not — timeout, fallback, rate limiting, adaptive
// concurrency — and composes them into one Executor. What it has no way to
// express is that a result may own something. Every policy that discards a
// result leaks it when the result is a connection, a file, or a lock:
//
//   - a hedge returns the first of N results and drops N-1;
//   - a retry configured with HandleResult discards a result that arrived
//     successfully but failed the predicate;
//   - a fallback returns its own value and drops the primary's;
//   - a timeout returns before a result that arrives anyway.
//
// None of those are bugs in failsafe-go; a library that treats R as an
// opaque value cannot know that dropping one costs something. This package
// supplies the missing half by making R an *ownership.Owner[T], which
// carries its own cleanup: whatever the chain does not hand back is
// released, and the release runs the Drop the caller already configured.
//
//	exec := failsafe.NewExecutor[*ownership.Owner[*Conn]](
//	    fallback.WithResult(spare),
//	    hedgepolicy.NewWithDelay[*ownership.Owner[*Conn]](50*time.Millisecond),
//	)
//	conn, err := failsafeown.Get(ctx, exec, dial) // losers are closed
//
// Import it only if you want failsafe-go's breadth; it is a separate module
// so the velocity library itself keeps no dependency on it.
package failsafeown

import (
	"context"
	"sync"

	"github.com/apsis-io/velocity/ownership"
	failsafe "github.com/failsafe-go/failsafe-go"
)

// Hooks lets a caller observe disposal without this package owning metrics
// state. Nil callbacks are skipped.
type Hooks[T any] struct {
	// OnDiscard fires for each owner the policy chain did not return, with
	// whatever Release reported. It is the only report of a failed release:
	// the caller has the result they asked for, and a policy that could not
	// clean up after itself is an operational event, not a call error.
	OnDiscard func(owner *ownership.Owner[T], err error)
}

// Get runs fn under exec and releases every owner produced along the way
// except the one returned. It is Executor.Get with the leak closed.
//
// fn may run several times, and concurrently with itself when the chain
// contains a hedge, so it must be safe to replay: give each call its own
// resource rather than returning the same owner twice. An owner returned
// beside an error is still tracked, since a failed attempt may have
// acquired something before failing.
//
// Disposal is not confined to the call. A hedge's losing attempt can return
// after Get has already answered; that owner is released as it arrives, on
// the goroutine that produced it.
func Get[T any](ctx context.Context, exec failsafe.Executor[*ownership.Owner[T]], fn func(context.Context) (*ownership.Owner[T], error), hooks Hooks[T]) (*ownership.Owner[T], error) {
	if ctx == nil {
		return nil, context.Canceled
	}
	if exec == nil || fn == nil {
		return nil, ownership.ErrNilOption
	}
	track := &tracker[T]{hooks: hooks}
	result, err := exec.WithContext(ctx).Get(func() (*ownership.Owner[T], error) {
		owner, err := fn(ctx)
		track.record(owner)
		return owner, err
	})
	track.settle(result)
	return result, err
}

// tracker holds the owners fn produced until the executor says which one
// was kept, then releases the rest — including any that arrive afterwards.
type tracker[T any] struct {
	hooks Hooks[T]

	mu      sync.Mutex
	pending []*ownership.Owner[T]
	winner  *ownership.Owner[T]
	settled bool
}

func (t *tracker[T]) record(owner *ownership.Owner[T]) {
	if owner == nil {
		return
	}
	t.mu.Lock()
	if !t.settled {
		t.pending = append(t.pending, owner)
		t.mu.Unlock()
		return
	}
	// The executor has already answered. Anything arriving now lost, unless
	// it is somehow the very owner that was returned.
	settledWinner := t.winner
	t.mu.Unlock()
	if owner != settledWinner {
		t.release(owner)
	}
}

// settle records which owner the executor returned and releases the others.
func (t *tracker[T]) settle(winner *ownership.Owner[T]) {
	t.mu.Lock()
	t.settled = true
	t.winner = winner
	pending := t.pending
	t.pending = nil
	t.mu.Unlock()
	for _, owner := range pending {
		if owner != winner {
			t.release(owner)
		}
	}
}

func (t *tracker[T]) release(owner *ownership.Owner[T]) {
	err := owner.Release()
	if hook := t.hooks.OnDiscard; hook != nil {
		hook(owner, err)
	}
}
