package resilience_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/apsis-io/velocity/resilience"
)

// stepClock advances only when told to, so transitions are deterministic.
type stepClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *stepClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *stepClock) Sleep(ctx context.Context, d time.Duration) error {
	c.Advance(d)
	return ctx.Err()
}

func (c *stepClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

var errBoom = errors.New("boom")

func fail(context.Context) (int, error)    { return 0, errBoom }
func succeed(context.Context) (int, error) { return 1, nil }

type transition struct {
	from, to resilience.State
	counts   resilience.Counts
}

type recorder struct {
	mu          sync.Mutex
	transitions []transition
}

func (r *recorder) hooks() resilience.BreakerHooks {
	return resilience.BreakerHooks{OnStateChange: func(from, to resilience.State, counts resilience.Counts) {
		r.mu.Lock()
		r.transitions = append(r.transitions, transition{from, to, counts})
		r.mu.Unlock()
	}}
}

func (r *recorder) all() []transition {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]transition(nil), r.transitions...)
}

func newBreaker(t *testing.T, clock *stepClock, rec *recorder, policy resilience.BreakerPolicy) *resilience.Breaker {
	t.Helper()
	policy.Clock = clock
	if rec != nil {
		policy.Hooks = rec.hooks()
	}
	b, err := resilience.NewBreaker(policy)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestNewBreakerValidation(t *testing.T) {
	trip := resilience.ConsecutiveFailures(1)
	tests := []struct {
		name   string
		policy resilience.BreakerPolicy
		want   error
	}{
		{"nil trip", resilience.BreakerPolicy{OpenFor: time.Second}, resilience.ErrNilTrip},
		{"zero open", resilience.BreakerPolicy{Trip: trip}, resilience.ErrInvalidBreaker},
		{"negative interval", resilience.BreakerPolicy{Trip: trip, OpenFor: time.Second, Interval: -1}, resilience.ErrInvalidBreaker},
		{"negative probes", resilience.BreakerPolicy{Trip: trip, OpenFor: time.Second, MaxProbes: -1}, resilience.ErrInvalidBreaker},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := resilience.NewBreaker(tt.policy)
			if !errors.Is(err, tt.want) || !errors.Is(err, resilience.ErrInvalidPolicy) {
				t.Fatalf("error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestBreakerLifecycle(t *testing.T) {
	clock := &stepClock{}
	var rec recorder
	b := newBreaker(t, clock, &rec, resilience.BreakerPolicy{
		Trip:    resilience.ConsecutiveFailures(2),
		OpenFor: time.Minute,
	})
	ctx := context.Background()

	// Closed: failures count, the trip fires on the second.
	if _, err := b.Do(ctx, fail); !errors.Is(err, errBoom) {
		t.Fatalf("first failure = %v", err)
	}
	if got := b.State(); got != resilience.Closed {
		t.Fatalf("state after one failure = %v", got)
	}
	if _, err := b.Do(ctx, fail); !errors.Is(err, errBoom) {
		t.Fatalf("second failure = %v", err)
	}
	if got := b.State(); got != resilience.Open {
		t.Fatalf("state after trip = %v", got)
	}

	// Open: rejected without running, with the remaining wait.
	clock.Advance(20 * time.Second)
	ran := false
	_, err := b.Do(ctx, func(context.Context) (int, error) { ran = true; return 1, nil })
	var be *resilience.BreakerError
	if !errors.Is(err, resilience.ErrOpen) || !errors.As(err, &be) || ran {
		t.Fatalf("open Do = %v, ran=%t", err, ran)
	}
	if be.State != resilience.Open || be.RetryAfter != 40*time.Second {
		t.Fatalf("rejection = %+v", be)
	}

	// After OpenFor: half-open, one probe admitted; a failed probe reopens.
	clock.Advance(40 * time.Second)
	if got := b.State(); got != resilience.HalfOpen {
		t.Fatalf("state after OpenFor = %v", got)
	}
	if _, err := b.Do(ctx, fail); !errors.Is(err, errBoom) {
		t.Fatalf("failed probe = %v", err)
	}
	if got := b.State(); got != resilience.Open {
		t.Fatalf("state after failed probe = %v", got)
	}

	// A successful probe closes and clears the counts.
	clock.Advance(time.Minute)
	if value, err := b.Do(ctx, succeed); err != nil || value != 1 {
		t.Fatalf("successful probe = (%d, %v)", value, err)
	}
	if got := b.State(); got != resilience.Closed {
		t.Fatalf("state after successful probe = %v", got)
	}
	if got := b.Counts(); got != (resilience.Counts{}) {
		t.Fatalf("counts after close = %+v", got)
	}

	want := []transition{
		{resilience.Closed, resilience.Open, resilience.Counts{Requests: 2, Failures: 2, ConsecutiveFailures: 2}},
		{resilience.Open, resilience.HalfOpen, resilience.Counts{}},
		{resilience.HalfOpen, resilience.Open, resilience.Counts{Requests: 1, Failures: 1, ConsecutiveFailures: 1}},
		{resilience.Open, resilience.HalfOpen, resilience.Counts{}},
		{resilience.HalfOpen, resilience.Closed, resilience.Counts{Requests: 1, Successes: 1, ConsecutiveSuccesses: 1}},
	}
	got := rec.all()
	if len(got) != len(want) {
		t.Fatalf("transitions = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("transition %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestBreakerHalfOpenBoundsProbes(t *testing.T) {
	clock := &stepClock{}
	b := newBreaker(t, clock, nil, resilience.BreakerPolicy{
		Trip:             resilience.ConsecutiveFailures(1),
		OpenFor:          time.Second,
		MaxProbes:        2,
		SuccessesToClose: 2,
	})
	ctx := context.Background()
	if _, err := b.Do(ctx, fail); !errors.Is(err, errBoom) {
		t.Fatal(err)
	}
	clock.Advance(time.Second)

	first, err := b.Allow()
	if err != nil {
		t.Fatalf("first probe = %v", err)
	}
	second, err := b.Allow()
	if err != nil {
		t.Fatalf("second probe = %v", err)
	}
	_, err = b.Allow()
	var be *resilience.BreakerError
	if !errors.Is(err, resilience.ErrOpen) || !errors.As(err, &be) || be.State != resilience.HalfOpen {
		t.Fatalf("third probe = %v, want half-open rejection", err)
	}

	// One success is not enough to close; the slot it frees is reusable.
	first(nil)
	if got := b.State(); got != resilience.HalfOpen {
		t.Fatalf("state after one success = %v", got)
	}
	third, err := b.Allow()
	if err != nil {
		t.Fatalf("probe after freed slot = %v", err)
	}
	// Reporting twice is ignored, so the count and slot do not double.
	first(nil)
	if got := b.Counts(); got.Successes != 1 {
		t.Fatalf("counts after duplicate report = %+v", got)
	}
	third(nil)
	if got := b.State(); got != resilience.Closed {
		t.Fatalf("state after two successes = %v", got)
	}
	// A report from before the transition belongs to a judged window.
	second(errBoom)
	if got := b.State(); got != resilience.Closed {
		t.Fatalf("stale failure reopened: %v", got)
	}
	if got := b.Counts(); got != (resilience.Counts{}) {
		t.Fatalf("stale report counted: %+v", got)
	}
}

func TestBreakerHalfOpenProbeBoundHoldsUnderContention(t *testing.T) {
	const probes = 3
	clock := &stepClock{}
	b := newBreaker(t, clock, nil, resilience.BreakerPolicy{
		Trip:             resilience.ConsecutiveFailures(1),
		OpenFor:          time.Second,
		MaxProbes:        probes,
		SuccessesToClose: probes,
	})
	ctx := context.Background()
	_, _ = b.Do(ctx, fail)
	clock.Advance(time.Second)

	var active, peak atomic.Int32
	release := make(chan struct{})
	var wg sync.WaitGroup
	var admitted, rejected atomic.Int32
	for range 50 {
		wg.Go(func() {
			_, err := b.Do(ctx, func(context.Context) (int, error) {
				n := active.Add(1)
				for {
					old := peak.Load()
					if n <= old || peak.CompareAndSwap(old, n) {
						break
					}
				}
				<-release
				active.Add(-1)
				return 1, nil
			})
			if err == nil {
				admitted.Add(1)
			} else if errors.Is(err, resilience.ErrOpen) {
				rejected.Add(1)
			} else {
				t.Error(err)
			}
		})
	}
	// Give every goroutine a chance to reach Allow before the probes finish.
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()

	if peak.Load() > probes {
		t.Fatalf("peak concurrent probes = %d, want <= %d", peak.Load(), probes)
	}
	// A goroutine scheduled only after the probes closed the breaker is
	// admitted too, so the exact split is not asserted; the bound above is.
	if admitted.Load() < probes || admitted.Load()+rejected.Load() != 50 {
		t.Fatalf("admitted %d, rejected %d", admitted.Load(), rejected.Load())
	}
	if got := b.State(); got != resilience.Closed {
		t.Fatalf("state after %d successful probes = %v", probes, got)
	}
}

func TestBreakerIntervalResetsClosedCounts(t *testing.T) {
	clock := &stepClock{}
	b := newBreaker(t, clock, nil, resilience.BreakerPolicy{
		Trip:     resilience.FailureRatio(0.5, 4),
		OpenFor:  time.Second,
		Interval: 10 * time.Second,
	})
	ctx := context.Background()
	for range 3 {
		_, _ = b.Do(ctx, fail)
	}
	if got := b.Counts(); got.Failures != 3 {
		t.Fatalf("counts = %+v", got)
	}
	clock.Advance(10 * time.Second)
	if got := b.Counts(); got != (resilience.Counts{}) {
		t.Fatalf("counts after interval = %+v", got)
	}
	// Three of the four in the new window: the ratio trips on the fourth
	// request, but the old window's failures no longer count toward it.
	_, _ = b.Do(ctx, succeed)
	_, _ = b.Do(ctx, succeed)
	_, _ = b.Do(ctx, fail)
	if got := b.State(); got != resilience.Closed {
		t.Fatalf("tripped on 1/3: %v", got)
	}
	_, _ = b.Do(ctx, fail)
	if got := b.State(); got != resilience.Open {
		t.Fatalf("did not trip on 2/4: %v", got)
	}
}

func TestBreakerFailureClassifierAndDoneContext(t *testing.T) {
	clock := &stepClock{}
	b := newBreaker(t, clock, nil, resilience.BreakerPolicy{
		Trip:    resilience.ConsecutiveFailures(1),
		OpenFor: time.Second,
		Failure: func(err error) bool { return !errors.Is(err, context.Canceled) },
	})
	_, err := b.Do(context.Background(), func(context.Context) (int, error) { return 0, context.Canceled })
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if got := b.Counts(); got.Requests != 1 || got.Failures != 0 || got.Successes != 1 {
		t.Fatalf("ignored error counted as failure: %+v", got)
	}

	ctx, cancel := context.WithCancelCause(context.Background())
	cause := errors.New("gone")
	cancel(cause)
	ran := false
	_, err = b.Do(ctx, func(context.Context) (int, error) { ran = true; return 1, nil })
	if !errors.Is(err, cause) || ran {
		t.Fatalf("done context Do = %v, ran=%t", err, ran)
	}
	if got := b.Counts(); got.Requests != 1 {
		t.Fatalf("done context counted: %+v", got)
	}
}

func TestBreakerPanicReleasesProbeSlot(t *testing.T) {
	clock := &stepClock{}
	b := newBreaker(t, clock, nil, resilience.BreakerPolicy{
		Trip:    resilience.ConsecutiveFailures(1),
		OpenFor: time.Second,
	})
	ctx := context.Background()
	_, _ = b.Do(ctx, fail)
	clock.Advance(time.Second)
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("panic did not propagate")
			}
		}()
		_, _ = b.Do(ctx, func(context.Context) (int, error) { panic("boom") })
	}()
	// The panicking probe counted as a failure, so the breaker reopened
	// rather than leaving the slot occupied in HalfOpen forever.
	if got := b.State(); got != resilience.Open {
		t.Fatalf("state after panicking probe = %v", got)
	}
}

func TestBreakerResetClosesAndNotifies(t *testing.T) {
	clock := &stepClock{}
	var rec recorder
	b := newBreaker(t, clock, &rec, resilience.BreakerPolicy{
		Trip:    resilience.ConsecutiveFailures(1),
		OpenFor: time.Hour,
	})
	_, _ = b.Do(context.Background(), fail)
	b.Reset()
	if got := b.State(); got != resilience.Closed {
		t.Fatalf("state after Reset = %v", got)
	}
	if got := rec.all(); len(got) != 2 || got[1].from != resilience.Open || got[1].to != resilience.Closed {
		t.Fatalf("transitions = %+v", got)
	}
}

func TestBreakerDoValidation(t *testing.T) {
	b := newBreaker(t, &stepClock{}, nil, resilience.BreakerPolicy{
		Trip:    resilience.ConsecutiveFailures(1),
		OpenFor: time.Second,
	})
	if _, err := b.Do(context.Background(), (func(context.Context) (int, error))(nil)); !errors.Is(err, resilience.ErrNilFunction) {
		t.Fatalf("nil fn = %v", err)
	}
	var nilCtx context.Context // the case under test; a literal nil trips SA1012
	if _, err := b.Do(nilCtx, succeed); !errors.Is(err, resilience.ErrInvalidPolicy) {
		t.Fatalf("nil ctx = %v", err)
	}
}

func TestBreakerStateString(t *testing.T) {
	for state, want := range map[resilience.State]string{resilience.Closed: "closed", resilience.Open: "open", resilience.HalfOpen: "half-open", resilience.State(9): "unknown"} {
		if got := state.String(); got != want {
			t.Fatalf("%d.String() = %q, want %q", state, got, want)
		}
	}
}

// Retry and Breaker compose: the breaker's rejection is not worth retrying
// because it cannot change until the clock does.
func TestRetryStopsOnOpenBreaker(t *testing.T) {
	clock := &stepClock{}
	b := newBreaker(t, clock, nil, resilience.BreakerPolicy{
		Trip:    resilience.ConsecutiveFailures(2),
		OpenFor: time.Minute,
	})
	calls := 0
	_, err := resilience.Retry(context.Background(), resilience.Policy{
		MaxAttempts: 10,
		Retryable:   func(err error) bool { return !errors.Is(err, resilience.ErrOpen) },
		Clock:       clock,
	}, func(ctx context.Context) (int, error) {
		return b.Do(ctx, func(context.Context) (int, error) { calls++; return 0, errBoom })
	})
	if !errors.Is(err, resilience.ErrOpen) || calls != 2 {
		t.Fatalf("Retry = %v after %d calls, want ErrOpen after 2", err, calls)
	}
}
