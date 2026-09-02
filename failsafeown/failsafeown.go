// Package failsafeown runs failsafe-go policies over owned resources,
// releasing every result the policy chain produced but did not return.
//
// failsafe-go supplies the policies velocity's resilience package
// deliberately does not — timeout, fallback, rate limiting, adaptive
// concurrency — and composes them into one Executor. What it has no way to
// express is that a result may own something. Every policy that discards a
// result leaks it when the result is a connection, a lock, a file handle,
// or filesystem artifacts such as a temp directory and the blob inside it —
// the last of which leak silently and permanently, with no pool or GC to
// bound them:
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
//	conn, err := failsafeown.Get(ctx, exec, dial, failsafeown.Hooks[*Conn]{})
//	// every connection the chain dialled and dropped is closed
//
// Use GetWithExecution when the attempts are not interchangeable — a hedge
// that races a peer against an origin has to know which one it is.
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
// The context fn receives is the execution's, not the one passed here, so
// it is cancelled when the policy chain gives up on that attempt — a
// hedge's loser or a timeout's overrun stops rather than running on.
//
// Disposal is not confined to the call. A hedge's losing attempt can return
// after Get has already answered; that owner is released as it arrives, on
// the goroutine that produced it.
func Get[T any](ctx context.Context, exec failsafe.Executor[*ownership.Owner[T]], fn func(context.Context) (*ownership.Owner[T], error), hooks Hooks[T]) (*ownership.Owner[T], error) {
	if fn == nil {
		return nil, ownership.ErrNilOption
	}
	return GetWithExecution(ctx, exec, func(e failsafe.Execution[*ownership.Owner[T]]) (*ownership.Owner[T], error) {
		return fn(executionContext(ctx, e))
	}, hooks)
}

// GetWithExecution is Get for attempts that are not interchangeable. It
// hands fn failsafe's Execution, so the attempt can tell which one it is
// and dispatch accordingly.
//
// A hedge whose attempts differ is the common case rather than the exotic
// one, since replicas usually have addresses: racing a peer against an
// origin, or a warm cache against a cold read, needs to know which arm it
// is. Deriving that from a counter in the closure works only while nothing
// else in the chain also reruns fn; add a retry and the counter silently
// stops meaning what it did.
//
//	policy := hedgepolicy.NewBuilderWithDelay[*ownership.Owner[*Layer]](0).
//	    // Without this, failsafe cancels the race on ANY result, so the arm
//	    // that fails fastest ends the one that would have succeeded.
//	    CancelIf(func(_ *ownership.Owner[*Layer], err error) bool { return err == nil }).
//	    Build()
//
//	owner, err := failsafeown.GetWithExecution(ctx, failsafe.With(policy),
//	    func(e failsafe.Execution[*ownership.Owner[*Layer]]) (*ownership.Owner[*Layer], error) {
//	        if e.IsHedge() {
//	            return fetchFromRegistry(e.Context())
//	        }
//	        return fetchFromPeers(e.Context())
//	    }, hooks)
//
// Use IsHedge to tell the arms apart, not Hedges. Hedges is a count shared
// by every attempt — "how many hedges exist", including ones in progress —
// so with a short delay both arms read the same number, both take the same
// branch, and the branch nobody took simply never runs. That is a hang
// rather than a wrong answer, and it does not reproduce under a long delay
// because then the primary reads the counter before the hedge exists.
// IsHedge is a per-attempt bool and is the only discriminator that holds.
// Retries and IsRetry are the same distinction for retries.
//
// Disposal is identical to Get's: whichever attempt loses, its owner is
// released, whenever it arrives.
func GetWithExecution[T any](ctx context.Context, exec failsafe.Executor[*ownership.Owner[T]], fn func(failsafe.Execution[*ownership.Owner[T]]) (*ownership.Owner[T], error), hooks Hooks[T]) (*ownership.Owner[T], error) {
	if ctx == nil {
		return nil, context.Canceled
	}
	if exec == nil || fn == nil {
		return nil, ownership.ErrNilOption
	}
	track := &tracker[T]{hooks: hooks}
	result, err := exec.WithContext(ctx).GetWithExecution(func(e failsafe.Execution[*ownership.Owner[T]]) (*ownership.Owner[T], error) {
		owner, err := fn(e)
		track.record(owner)
		return owner, err
	})
	track.settle(result)
	return result, err
}

// executionContext prefers the execution's context, which carries the
// policy chain's cancellation, and falls back to the caller's if a policy
// ever hands back none.
func executionContext[T any](ctx context.Context, e failsafe.Execution[*ownership.Owner[T]]) context.Context {
	if inner := e.Context(); inner != nil {
		return inner
	}
	return ctx
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
