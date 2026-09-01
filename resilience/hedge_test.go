package resilience_test

import (
	"context"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/apsis-io/velocity/resilience"
)

func fixedDelay(d time.Duration) resilience.Backoff {
	return func(int) time.Duration { return d }
}

// A slow first attempt is overtaken by the hedge, and the slow one's result
// is discarded rather than leaked when it finally arrives.
func TestHedgeSecondAttemptWinsAndFirstIsDiscarded(t *testing.T) {
	release := make(chan struct{})
	var discarded chan int = make(chan int, 1)
	policy := resilience.HedgePolicy[int]{
		MaxAttempts: 2,
		Delay:       fixedDelay(5 * time.Millisecond),
		Discard:     func(v int) error { discarded <- v; return nil },
	}
	value, err := resilience.Hedge(context.Background(), policy, func(ctx context.Context, attempt int) (int, error) {
		if attempt == 0 {
			<-release
			return 100, nil // arrives after the hedge has already won
		}
		return 200, nil
	})
	if err != nil || value != 200 {
		t.Fatalf("Hedge = (%d, %v), want the hedge's 200", value, err)
	}
	close(release)
	select {
	case got := <-discarded:
		if got != 100 {
			t.Fatalf("discarded %d, want the losing 100", got)
		}
	case <-time.After(time.Second):
		t.Fatal("the losing attempt's result was never discarded")
	}
}

// A first attempt that returns before the delay elapses is the whole
// execution: no hedge is ever started.
func TestHedgeFastFirstAttemptStartsNoHedge(t *testing.T) {
	var attempts atomic.Int32
	policy := resilience.HedgePolicy[int]{
		MaxAttempts: 4,
		Delay:       fixedDelay(time.Hour),
	}
	value, err := resilience.Hedge(context.Background(), policy, func(context.Context, int) (int, error) {
		attempts.Add(1)
		return 7, nil
	})
	if err != nil || value != 7 || attempts.Load() != 1 {
		t.Fatalf("Hedge = (%d, %v) after %d attempts", value, err, attempts.Load())
	}
}

// A failure is evidence, so the next attempt starts at once rather than
// waiting out a delay that exists to detect slowness.
func TestHedgeFailureStartsNextAttemptWithoutWaiting(t *testing.T) {
	boom := errors.New("boom")
	var attempts atomic.Int32
	start := time.Now()
	policy := resilience.HedgePolicy[string]{
		MaxAttempts: 3,
		Delay:       fixedDelay(30 * time.Second),
	}
	value, err := resilience.Hedge(context.Background(), policy, func(_ context.Context, attempt int) (string, error) {
		if attempts.Add(1) < 3 {
			return "", boom
		}
		return "third", nil
	})
	if err != nil || value != "third" {
		t.Fatalf("Hedge = (%q, %v)", value, err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("took %v; failures waited out the hedge delay", elapsed)
	}
}

func TestHedgeAllAttemptsFailJoinsErrors(t *testing.T) {
	first, second := errors.New("first"), errors.New("second")
	errs := []error{first, second}
	policy := resilience.HedgePolicy[int]{MaxAttempts: 2, Delay: fixedDelay(time.Millisecond)}
	_, err := resilience.Hedge(context.Background(), policy, func(_ context.Context, attempt int) (int, error) {
		return 0, errs[attempt]
	})
	var re *resilience.RetryError
	if !errors.As(err, &re) || re.Attempts != 2 {
		t.Fatalf("Hedge = %v, want a RetryError over 2 attempts", err)
	}
	if !errors.Is(err, first) || !errors.Is(err, second) {
		t.Fatalf("error = %v, want both causes", err)
	}
}

// A non-retryable error is the answer, not something to hedge past.
func TestHedgeStopsOnNonRetryableError(t *testing.T) {
	fatal := errors.New("bad request")
	var attempts atomic.Int32
	policy := resilience.HedgePolicy[int]{
		MaxAttempts: 4,
		Delay:       fixedDelay(time.Millisecond),
		Retryable:   func(err error) bool { return !errors.Is(err, fatal) },
	}
	_, err := resilience.Hedge(context.Background(), policy, func(context.Context, int) (int, error) {
		attempts.Add(1)
		return 0, fatal
	})
	if !errors.Is(err, fatal) || attempts.Load() != 1 {
		t.Fatalf("Hedge = %v after %d attempts, want the fatal error after 1", err, attempts.Load())
	}
	var re *resilience.RetryError
	if errors.As(err, &re) {
		t.Fatal("a non-retryable error was wrapped as a RetryError")
	}
}

// The losing attempts see their context cancelled, so a cooperative
// operation stops rather than running to completion in the background.
func TestHedgeCancelsLosingAttempts(t *testing.T) {
	stopped := make(chan struct{})
	policy := resilience.HedgePolicy[int]{MaxAttempts: 2, Delay: fixedDelay(5 * time.Millisecond)}
	value, err := resilience.Hedge(context.Background(), policy, func(ctx context.Context, attempt int) (int, error) {
		if attempt == 0 {
			<-ctx.Done()
			close(stopped)
			return 0, ctx.Err()
		}
		return 1, nil
	})
	if err != nil || value != 1 {
		t.Fatalf("Hedge = (%d, %v)", value, err)
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("the losing attempt was not cancelled")
	}
}

func TestHedgeHonoursCallerContext(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	cause := errors.New("caller gave up")
	policy := resilience.HedgePolicy[int]{MaxAttempts: 3, Delay: fixedDelay(time.Millisecond)}
	go func() { time.Sleep(10 * time.Millisecond); cancel(cause) }()
	_, err := resilience.Hedge(ctx, policy, func(ctx context.Context, _ int) (int, error) {
		<-ctx.Done()
		return 0, ctx.Err()
	})
	if !errors.Is(err, cause) {
		t.Fatalf("Hedge = %v, want the caller's cause", err)
	}
}

func TestHedgeValidation(t *testing.T) {
	valid := func(context.Context, int) (int, error) { return 0, nil }
	for _, tt := range []struct {
		name   string
		policy resilience.HedgePolicy[int]
		fn     func(context.Context, int) (int, error)
		want   error
	}{
		{"one attempt", resilience.HedgePolicy[int]{MaxAttempts: 1, Delay: fixedDelay(0)}, valid, resilience.ErrInvalidPolicy},
		{"no delay", resilience.HedgePolicy[int]{MaxAttempts: 2}, valid, resilience.ErrInvalidPolicy},
		{"nil fn", resilience.HedgePolicy[int]{MaxAttempts: 2, Delay: fixedDelay(0)}, nil, resilience.ErrNilFunction},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := resilience.Hedge(context.Background(), tt.policy, tt.fn); !errors.Is(err, tt.want) {
				t.Fatalf("Hedge = %v, want %v", err, tt.want)
			}
		})
	}
	var nilCtx context.Context // a literal nil trips SA1012
	if _, err := resilience.Hedge(nilCtx, resilience.HedgePolicy[int]{MaxAttempts: 2, Delay: fixedDelay(0)}, valid); !errors.Is(err, resilience.ErrInvalidPolicy) {
		t.Fatalf("nil ctx = %v", err)
	}
}

// The budget is what stops a slow dependency from being handed several
// times its usual load at the worst possible moment.
func TestHedgeBudgetBoundsExtraLoad(t *testing.T) {
	budget, err := resilience.NewHedgeBudget(0.25, 10)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resilience.NewHedgeBudget(0, 1); !errors.Is(err, resilience.ErrInvalidBudget) {
		t.Fatalf("zero ratio = %v", err)
	}
	if _, err := resilience.NewHedgeBudget(0.5, 0); !errors.Is(err, resilience.ErrInvalidBudget) {
		t.Fatalf("zero burst = %v", err)
	}

	policy := resilience.HedgePolicy[int]{
		MaxAttempts: 2,
		Delay:       fixedDelay(0), // hedge immediately, if allowed
		Budget:      budget,
	}
	var attempts atomic.Int64
	release := make(chan struct{})
	var wg sync.WaitGroup
	// Twenty executions at a 0.25 ratio fund five hedges, so the dependency
	// sees at most 25 attempts rather than 40.
	for range 20 {
		wg.Go(func() {
			_, _ = resilience.Hedge(context.Background(), policy, func(ctx context.Context, attempt int) (int, error) {
				attempts.Add(1)
				if attempt == 0 {
					select {
					case <-release:
					case <-ctx.Done():
					}
				}
				return attempt, nil
			})
		})
	}
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()

	total := attempts.Load()
	if total > 25 {
		t.Fatalf("attempts = %d for 20 executions at ratio 0.25, want at most 25", total)
	}
	if total <= 20 {
		t.Fatalf("attempts = %d, want some hedges to have been funded", total)
	}
}

// An empty budget refuses every hedge, and the execution must still finish
// rather than wait for a hedge that will never be funded.
func TestHedgeEmptyBudgetDoesNotHang(t *testing.T) {
	budget, err := resilience.NewHedgeBudget(0.01, 1)
	if err != nil {
		t.Fatal(err)
	}
	boom := errors.New("boom")
	var attempts atomic.Int32
	policy := resilience.HedgePolicy[int]{
		MaxAttempts: 5,
		Delay:       fixedDelay(0),
		Budget:      budget,
	}
	done := make(chan error, 1)
	go func() {
		_, err := resilience.Hedge(context.Background(), policy, func(context.Context, int) (int, error) {
			attempts.Add(1)
			return 0, boom
		})
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, boom) {
			t.Fatalf("Hedge = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Hedge hung waiting for a hedge the budget would never fund")
	}
	if got := attempts.Load(); got != 1 {
		t.Fatalf("attempts = %d, want the single funded one", got)
	}
	if tokens := budget.Tokens(); tokens < 0 || tokens > 1 {
		t.Fatalf("tokens = %v, want within the burst", tokens)
	}
}

// A nil budget permits every hedge, which is the documented default.
func TestNilHedgeBudgetPermits(t *testing.T) {
	var budget *resilience.HedgeBudget
	if budget.Tokens() != 0 {
		t.Fatal("nil budget reported tokens")
	}
	policy := resilience.HedgePolicy[int]{MaxAttempts: 3, Delay: fixedDelay(0), Budget: budget}
	var started atomic.Int32
	release := make(chan struct{})
	// Keyed off the attempt index, not a shared counter: which goroutine
	// increments a counter first is not ordered, but the index is.
	value, err := resilience.Hedge(context.Background(), policy, func(ctx context.Context, attempt int) (int, error) {
		started.Add(1)
		if attempt < 2 {
			select {
			case <-release:
			case <-ctx.Done():
			}
			return 0, ctx.Err()
		}
		return attempt, nil
	})
	close(release)
	if err != nil || value != 2 {
		t.Fatalf("Hedge = (%d, %v), want the third attempt", value, err)
	}
	if got := started.Load(); got != 3 {
		t.Fatalf("started = %d attempts, want all 3 funded by a nil budget", got)
	}
}

func TestHedgeHooksReportEveryAttempt(t *testing.T) {
	var mu sync.Mutex
	var started []int
	var hedges []bool
	var wonBy int = -1
	var discards int
	policy := resilience.HedgePolicy[int]{
		MaxAttempts: 2,
		Delay:       fixedDelay(5 * time.Millisecond),
		Discard:     func(int) error { return errors.New("close failed") },
		Hooks: resilience.HedgeHooks{
			OnAttempt: func(attempt int, hedge bool) {
				mu.Lock()
				started = append(started, attempt)
				hedges = append(hedges, hedge)
				mu.Unlock()
			},
			OnAttemptComplete: func(attempt int, _ time.Duration, _ error, won bool) {
				mu.Lock()
				if won {
					wonBy = attempt
				}
				mu.Unlock()
			},
			OnDiscard: func(_ int, err error) {
				mu.Lock()
				if err != nil {
					discards++
				}
				mu.Unlock()
			},
		},
	}
	release := make(chan struct{})
	_, err := resilience.Hedge(context.Background(), policy, func(_ context.Context, attempt int) (int, error) {
		if attempt == 0 {
			<-release
			return 1, nil
		}
		return 2, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	close(release)
	deadline := time.After(time.Second)
	for {
		mu.Lock()
		seen := discards
		mu.Unlock()
		if seen == 1 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("the failed discard was never reported")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(started, []int{0, 1}) || !slices.Equal(hedges, []bool{false, true}) {
		t.Fatalf("attempts = %v, hedge flags = %v", started, hedges)
	}
	if wonBy != 1 {
		t.Fatalf("won by attempt %d, want 1", wonBy)
	}
}

// Hedge composes with Breaker: each attempt reports to the breaker, so a
// dependency that is failing trips it rather than being hedged harder.
func TestHedgeInsideBreaker(t *testing.T) {
	clock := resilience.NewManualClock(time.Unix(0, 0))
	breaker, err := resilience.NewBreaker(resilience.BreakerPolicy{
		Trip:    resilience.ConsecutiveFailures(2),
		OpenFor: time.Minute,
		Clock:   clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	boom := errors.New("boom")
	policy := resilience.HedgePolicy[int]{MaxAttempts: 2, Delay: fixedDelay(0)}
	_, err = resilience.Hedge(context.Background(), policy, func(ctx context.Context, _ int) (int, error) {
		return breaker.Do(ctx, func(context.Context) (int, error) { return 0, boom })
	})
	if !errors.Is(err, boom) {
		t.Fatalf("Hedge = %v", err)
	}
	if got := breaker.State(); got != resilience.Open {
		t.Fatalf("breaker = %v after two failed attempts, want open", got)
	}
}
