package resilience

import (
	"context"
	"errors"
	"sync"
	"time"
)

// State is a circuit breaker position.
type State int

const (
	// Closed admits every call and counts outcomes toward tripping.
	Closed State = iota
	// Open rejects every call with ErrOpen until OpenFor has elapsed.
	Open
	// HalfOpen admits up to MaxProbes concurrent calls to test recovery: any
	// failure reopens, SuccessesToClose successes close.
	HalfOpen
)

func (s State) String() string {
	switch s {
	case Closed:
		return "closed"
	case Open:
		return "open"
	case HalfOpen:
		return "half-open"
	}
	return "unknown"
}

// Counts are the outcomes recorded since the last state change or Interval
// reset. They describe completed calls only; an admitted call that has not
// reported yet is in none of them.
type Counts struct {
	Requests             int
	Successes            int
	Failures             int
	ConsecutiveSuccesses int
	ConsecutiveFailures  int
}

// Trip decides from the current Counts whether a Closed breaker should open.
// It is consulted after every failure while Closed.
type Trip func(Counts) bool

// ConsecutiveFailures trips after n failures in a row.
func ConsecutiveFailures(n int) Trip {
	return func(c Counts) bool { return c.ConsecutiveFailures >= n }
}

// FailureRatio trips once at least minRequests have completed and the
// failing fraction of them reaches ratio.
func FailureRatio(ratio float64, minRequests int) Trip {
	return func(c Counts) bool {
		return c.Requests >= minRequests && float64(c.Failures)/float64(c.Requests) >= ratio
	}
}

// BreakerHooks lets a caller observe transitions without the package owning
// metrics state. Nil callbacks are skipped. Hooks run outside the breaker's
// lock, on the goroutine whose call caused the transition.
type BreakerHooks struct {
	// OnStateChange reports each transition with the counts that ended the
	// previous state.
	OnStateChange func(from, to State, counts Counts)
}

// BreakerPolicy configures a Breaker. Trip and OpenFor are required.
type BreakerPolicy struct {
	// Trip decides when a Closed breaker opens.
	Trip Trip
	// OpenFor is how long the breaker stays Open before admitting probes.
	OpenFor time.Duration
	// Interval, when positive, resets Counts while Closed each time it
	// elapses, so a ratio Trip judges recent behaviour rather than the whole
	// history. Zero never resets until a transition.
	Interval time.Duration
	// MaxProbes bounds concurrent calls while HalfOpen; zero means one.
	MaxProbes int
	// SuccessesToClose is how many probes must succeed, with no failure in
	// between, to close again; zero means one.
	SuccessesToClose int
	// Failure decides whether an error counts against the breaker. Nil counts
	// every error, including the caller's own cancellation; most callers want
	// to exclude that, since it says nothing about the protected resource:
	//
	//	Failure: func(err error) bool { return !errors.Is(err, context.Canceled) }
	Failure Classifier
	Clock   Clock
	Hooks   BreakerHooks
}

// Breaker is a circuit breaker: it stops calling a resource that keeps
// failing, and after OpenFor lets a few calls through to find out whether it
// has recovered. Fail fast is the whole point, so a rejected call returns
// ErrOpen immediately; nothing in a Breaker waits.
//
// Transitions happen lazily, on the next call or State read after the
// relevant time has passed, so a Breaker needs no goroutine or timer.
type Breaker struct {
	policy BreakerPolicy
	clock  Clock

	mu         sync.Mutex
	state      State
	counts     Counts
	inflight   int    // HalfOpen probes admitted and not yet reported
	generation uint64 // reports from an earlier generation are ignored
	expiry     time.Time
}

// NewBreaker validates the policy and returns a Closed breaker.
func NewBreaker(policy BreakerPolicy) (*Breaker, error) {
	if policy.Trip == nil {
		return nil, &PolicyError{Cause: ErrNilTrip}
	}
	if policy.OpenFor <= 0 || policy.Interval < 0 || policy.MaxProbes < 0 || policy.SuccessesToClose < 0 {
		return nil, &PolicyError{Cause: ErrInvalidBreaker}
	}
	if policy.MaxProbes == 0 {
		policy.MaxProbes = 1
	}
	if policy.SuccessesToClose == 0 {
		policy.SuccessesToClose = 1
	}
	clock := policy.Clock
	if clock == nil {
		clock = RealClock()
	}
	b := &Breaker{policy: policy, clock: clock}
	b.enterLocked(Closed, clock.Now())
	return b, nil
}

// Do runs fn if the breaker admits it and reports the outcome. A rejection is
// a BreakerError satisfying errors.Is(err, ErrOpen); fn's own error is
// returned as is. A context that is already done is not counted as a
// failure, since the call never reached the resource.
//
// fn must return normally. A panic is recorded as a failure so the probe slot
// it held is not lost, and then propagates.
func (b *Breaker) Do[T any](ctx context.Context, fn func(context.Context) (T, error)) (T, error) {
	var zero T
	if ctx == nil {
		return zero, &PolicyError{Cause: context.Canceled}
	}
	if fn == nil {
		return zero, &PolicyError{Cause: ErrNilFunction}
	}
	if ctx.Err() != nil {
		return zero, context.Cause(ctx)
	}
	generation, err := b.admit()
	if err != nil {
		return zero, err
	}
	reported := false
	defer func() {
		if !reported {
			b.report(generation, errPanicked)
		}
	}()
	value, err := fn(ctx)
	reported = true
	b.report(generation, err)
	if err != nil {
		return zero, err
	}
	return value, nil
}

// Allow is Do for calls that cannot be wrapped in a function, such as a
// stream whose outcome is known only later. It admits or rejects now and
// returns a report to call exactly once with the outcome; later calls are
// ignored. A report that is never made leaves a HalfOpen probe slot occupied
// until the next transition, so prefer Do wherever it fits.
func (b *Breaker) Allow() (report func(error), err error) {
	generation, err := b.admit()
	if err != nil {
		return nil, err
	}
	var once sync.Once
	return func(err error) {
		once.Do(func() { b.report(generation, err) })
	}, nil
}

// admit decides whether a call may proceed and, if so, returns the generation
// its report must carry.
func (b *Breaker) admit() (uint64, error) {
	now := b.clock.Now()
	b.mu.Lock()
	transition := b.advanceLocked(now)
	switch b.state {
	case Open:
		retryAfter := b.expiry.Sub(now)
		b.mu.Unlock()
		b.notify(transition)
		return 0, &BreakerError{State: Open, RetryAfter: retryAfter}
	case HalfOpen:
		if b.inflight >= b.policy.MaxProbes {
			b.mu.Unlock()
			b.notify(transition)
			return 0, &BreakerError{State: HalfOpen}
		}
		b.inflight++
	}
	generation := b.generation
	b.mu.Unlock()
	b.notify(transition)
	return generation, nil
}

// State returns the current position, applying any transition that time has
// made due.
func (b *Breaker) State() State {
	now := b.clock.Now()
	b.mu.Lock()
	transition := b.advanceLocked(now)
	state := b.state
	b.mu.Unlock()
	b.notify(transition)
	return state
}

// Counts returns the outcomes recorded in the current state.
func (b *Breaker) Counts() Counts {
	now := b.clock.Now()
	b.mu.Lock()
	transition := b.advanceLocked(now)
	counts := b.counts
	b.mu.Unlock()
	b.notify(transition)
	return counts
}

// Reset forces the breaker Closed and clears its counts, for an operator who
// knows the resource is back before the breaker does.
func (b *Breaker) Reset() {
	now := b.clock.Now()
	b.mu.Lock()
	transition := b.enterLocked(Closed, now)
	b.mu.Unlock()
	b.notify(transition)
}

func (b *Breaker) report(generation uint64, err error) {
	now := b.clock.Now()
	b.mu.Lock()
	transition := b.advanceLocked(now)
	if generation != b.generation {
		// The state changed under this call; its outcome belongs to a
		// window that has already been judged.
		b.mu.Unlock()
		b.notify(transition)
		return
	}
	failed := err != nil && (b.policy.Failure == nil || b.policy.Failure(err))
	b.counts.Requests++
	if failed {
		b.counts.Failures++
		b.counts.ConsecutiveFailures++
		b.counts.ConsecutiveSuccesses = 0
	} else {
		b.counts.Successes++
		b.counts.ConsecutiveSuccesses++
		b.counts.ConsecutiveFailures = 0
	}
	switch b.state {
	case Closed:
		if failed && b.policy.Trip(b.counts) {
			transition = b.enterLocked(Open, now)
		}
	case HalfOpen:
		b.inflight--
		if failed {
			transition = b.enterLocked(Open, now)
		} else if b.counts.ConsecutiveSuccesses >= b.policy.SuccessesToClose {
			transition = b.enterLocked(Closed, now)
		}
	}
	b.mu.Unlock()
	b.notify(transition)
}

// advanceLocked applies whatever transition the clock has made due.
func (b *Breaker) advanceLocked(now time.Time) *stateChange {
	switch b.state {
	case Open:
		if !now.Before(b.expiry) {
			return b.enterLocked(HalfOpen, now)
		}
	case Closed:
		if b.policy.Interval > 0 && !now.Before(b.expiry) {
			// A new window, not a new state: no hook, but a new generation
			// so calls admitted under the old window do not count here.
			b.counts = Counts{}
			b.generation++
			b.expiry = now.Add(b.policy.Interval)
		}
	}
	return nil
}

type stateChange struct {
	from, to State
	counts   Counts
}

// enterLocked moves to state, starting a fresh generation and window.
func (b *Breaker) enterLocked(state State, now time.Time) *stateChange {
	change := &stateChange{from: b.state, to: state, counts: b.counts}
	b.state = state
	b.counts = Counts{}
	b.inflight = 0
	b.generation++
	switch state {
	case Open:
		b.expiry = now.Add(b.policy.OpenFor)
	case Closed:
		b.expiry = now.Add(b.policy.Interval)
	default:
		b.expiry = time.Time{}
	}
	if change.from == change.to {
		return nil
	}
	return change
}

// notify runs the hook for a transition, outside the lock.
func (b *Breaker) notify(change *stateChange) {
	if change == nil {
		return
	}
	if hook := b.policy.Hooks.OnStateChange; hook != nil {
		hook(change.from, change.to, change.counts)
	}
}

var errPanicked = errors.New("resilience: breaker callback panicked")
