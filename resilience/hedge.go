package resilience

import (
	"context"
	"errors"
	"sync"
	"time"
)

// HedgeHooks lets a caller observe hedged execution without the package
// owning metrics state. Nil callbacks are skipped.
type HedgeHooks struct {
	// OnAttempt fires as each attempt starts. hedge is false for the first
	// attempt and true for every speculative one.
	OnAttempt func(attempt int, hedge bool)
	// OnAttemptComplete fires as each attempt finishes, including attempts
	// that lose the race and those cancelled after a winner is chosen. won
	// reports whether this attempt's value was the one returned.
	OnAttemptComplete func(attempt int, duration time.Duration, err error, won bool)
	// OnDiscard fires for each losing result handed to Discard, with
	// whatever Discard returned. It is the only report of a disposal
	// failure: the caller already has their value, and a hedge that could
	// not clean up after itself is an operational event, not a call error.
	OnDiscard func(attempt int, err error)
}

// HedgePolicy configures Hedge. MaxAttempts and Delay are required.
type HedgePolicy[T any] struct {
	// MaxAttempts bounds attempts in total, the first included. It must be
	// at least 2; one attempt is not a hedge.
	MaxAttempts int
	// Delay reports how long to wait before starting attempt n while the
	// attempts already running have neither succeeded nor failed. It is the
	// same Backoff that Retry uses, so a fixed delay is
	// func(int) time.Duration { return d } and a widening one is
	// ExponentialBackoff.
	Delay Backoff
	// Budget bounds how much extra load hedging is allowed to create. A nil
	// Budget hedges whenever the policy says to, which multiplies load on
	// the very dependency that is already slow.
	Budget *HedgeBudget
	// Discard disposes the result of an attempt that arrived after the
	// winner, which is otherwise leaked: a hedge produces N results and
	// returns one. Nil means T needs no disposal. For an owned resource,
	// this is its Drop.
	Discard func(T) error
	// Retryable decides whether a failed attempt should be followed by
	// another. Nil hedges past every error. A non-retryable error ends the
	// execution immediately and is returned as is.
	Retryable Classifier
	Clock     Clock
	Hooks     HedgeHooks
}

// Hedge runs fn as a race against its own latency: the first attempt starts
// at once, and each later one starts when Delay says the ones already
// running have taken too long. The first success wins, the rest are
// cancelled, and any result that arrives anyway is handed to Discard.
//
//	value, err := resilience.Hedge(ctx, policy, func(ctx context.Context, attempt int) (*Response, error) {
//	    return client.Fetch(ctx, request)
//	})
//
// It is the tail-latency counterpart of Retry. Retry waits for an attempt to
// fail before starting the next and cannot help an attempt that is merely
// slow; Hedge starts the next while the previous is still running, so p99
// latency falls toward p50 at the cost of extra load — which is what Budget
// is for. An attempt that fails does not wait for its Delay: the next starts
// at once, since the evidence to act on has already arrived.
//
// fn receives the attempt number, zero-based, and a context that is
// cancelled once another attempt wins. It must be safe to run concurrently
// with itself: hedging replays the operation, so an operation that is not
// replay-safe must not be hedged.
func Hedge[T any](ctx context.Context, policy HedgePolicy[T], fn func(context.Context, int) (T, error)) (T, error) {
	var zero T
	if ctx == nil {
		return zero, &PolicyError{Cause: context.Canceled}
	}
	if fn == nil {
		return zero, &PolicyError{Cause: ErrNilFunction}
	}
	if policy.MaxAttempts < 2 || policy.Delay == nil {
		return zero, &PolicyError{Cause: ErrInvalidPolicy}
	}
	if err := ctx.Err(); err != nil {
		return zero, context.Cause(ctx)
	}
	clock := policy.Clock
	if clock == nil {
		clock = RealClock()
	}

	run := &hedgeRun[T]{
		policy:  policy,
		clock:   clock,
		fn:      fn,
		results: make(chan hedgeResult[T], policy.MaxAttempts),
		due:     make(chan struct{}, policy.MaxAttempts),
	}
	run.ctx, run.cancel = context.WithCancelCause(ctx)

	// This execution pays into the budget whether or not it ends up
	// hedging, since it is the traffic that earns the allowance.
	policy.Budget.credit()
	run.launch()
	value, err := run.collect(ctx)
	// Whatever happened, attempts may still be running. Reap them on one
	// goroutine that exits when the last one reports, discarding any result
	// that arrives too late to be returned.
	go run.reap()
	return value, err
}

type hedgeResult[T any] struct {
	attempt  int
	value    T
	err      error
	duration time.Duration
}

type hedgeRun[T any] struct {
	policy HedgePolicy[T]
	clock  Clock
	fn     func(context.Context, int) (T, error)

	ctx     context.Context
	cancel  context.CancelCauseFunc
	results chan hedgeResult[T]
	due     chan struct{} // one token per hedge whose delay has elapsed

	launched  int
	completed int
	timer     Timer
	won       int // attempt whose value was returned, -1 if none
}

// launch starts the next attempt and schedules the one after it.
func (r *hedgeRun[T]) launch() {
	attempt := r.launched
	r.launched++
	if hook := r.policy.Hooks.OnAttempt; hook != nil {
		hook(attempt, attempt > 0)
	}
	go func() {
		start := r.clock.Now()
		value, err := r.fn(r.ctx, attempt)
		r.results <- hedgeResult[T]{attempt: attempt, value: value, err: err, duration: r.clock.Now().Sub(start)}
	}()
	r.schedule()
}

// schedule arms the timer for the next hedge, if another is allowed.
func (r *hedgeRun[T]) schedule() {
	if r.launched >= r.policy.MaxAttempts {
		return
	}
	delay := r.policy.Delay(r.launched)
	r.timer = r.clock.AfterFunc(delay, func() {
		select {
		case r.due <- struct{}{}:
		default: // the buffer holds one token per possible hedge
		}
	})
}

// hedgeAllowed reports whether another speculative attempt may start, and
// charges the budget when it may.
func (r *hedgeRun[T]) hedgeAllowed() bool {
	return r.launched < r.policy.MaxAttempts && r.policy.Budget.withdraw()
}

// collect waits for the first success, for every attempt to fail, or for the
// caller's context. It returns the value to hand back.
func (r *hedgeRun[T]) collect(callerCtx context.Context) (T, error) {
	var zero T
	r.won = -1
	var errs []error
	for {
		select {
		case <-r.due:
			if r.hedgeAllowed() {
				r.launch()
			}
		case result := <-r.results:
			r.completed++
			if result.err == nil {
				r.won = result.attempt
				r.report(result, true)
				r.stop(nil)
				return result.value, nil
			}
			r.report(result, false)
			errs = append(errs, result.err)
			if r.policy.Retryable != nil && !r.policy.Retryable(result.err) {
				r.stop(result.err)
				return zero, result.err
			}
			// Evidence has arrived; do not wait out the remaining delay.
			if r.hedgeAllowed() {
				r.launch()
			}
			// Nothing is running and nothing may start: the budget can
			// refuse before MaxAttempts is reached, and waiting for a
			// hedge that will never be funded would hang.
			if r.completed == r.launched {
				r.stop(nil)
				return zero, &RetryError{Attempts: r.completed, Last: errors.Join(errs...)}
			}
		case <-callerCtx.Done():
			cause := context.Cause(callerCtx)
			r.stop(cause)
			return zero, cause
		}
	}
}

// stop ends the remaining attempts and disarms the pending hedge timer.
func (r *hedgeRun[T]) stop(cause error) {
	if r.timer != nil {
		r.timer.Stop()
	}
	r.cancel(cause)
}

func (r *hedgeRun[T]) report(result hedgeResult[T], won bool) {
	if hook := r.policy.Hooks.OnAttemptComplete; hook != nil {
		hook(result.attempt, result.duration, result.err, won)
	}
}

// reap drains the attempts still running after collect returned, discarding
// their results. It exits once every launched attempt has reported, so it
// cannot outlive the execution.
func (r *hedgeRun[T]) reap() {
	defer r.cancel(nil)
	for r.completed < r.launched {
		select {
		case result := <-r.results:
			r.completed++
			r.report(result, false)
			if result.err != nil || result.attempt == r.won {
				continue
			}
			if discard := r.policy.Discard; discard != nil {
				err := discard(result.value)
				if hook := r.policy.Hooks.OnDiscard; hook != nil {
					hook(result.attempt, err)
				}
			}
		case <-r.due:
			// A hedge came due as the winner was chosen; it is not started.
		}
	}
}

// HedgeBudget bounds the extra load hedging creates, as a fraction of the
// traffic that justified it. Each execution credits the budget once, and
// each speculative attempt spends one credit, so a ratio of 0.1 permits a
// hedge on roughly one call in ten however the delays fall.
//
// Without one, a dependency that has become slow enough to trigger hedging
// receives several times its usual load at exactly the wrong moment. Share
// one budget per dependency across every policy that hedges it.
//
// A nil *HedgeBudget permits every hedge, which is the right choice only
// when the operation is cheap or the caller is the only client.
type HedgeBudget struct {
	mu     sync.Mutex
	tokens float64
	ratio  float64
	burst  float64
}

// NewHedgeBudget permits hedges at ratio of total executions — 0.1 for ten
// percent — allowing up to burst of them back to back. The budget starts
// empty, so hedging begins only once traffic has paid for it.
func NewHedgeBudget(ratio float64, burst int) (*HedgeBudget, error) {
	if ratio <= 0 || ratio > 1 || burst <= 0 {
		return nil, &PolicyError{Cause: ErrInvalidBudget}
	}
	return &HedgeBudget{ratio: ratio, burst: float64(burst)}, nil
}

// credit adds one execution's worth of budget.
func (b *HedgeBudget) credit() {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.tokens = min(b.tokens+b.ratio, b.burst)
	b.mu.Unlock()
}

// withdraw spends one credit, reporting whether the hedge may start. A nil
// budget always permits.
func (b *HedgeBudget) withdraw() bool {
	if b == nil {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// Tokens reports the credits available for hedging now, which is how a test
// or a metric observes the budget without inferring it from behaviour.
func (b *HedgeBudget) Tokens() float64 {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.tokens
}
