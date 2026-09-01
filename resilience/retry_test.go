package resilience_test

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/apsis-io/velocity/resilience"
)

type fakeClock struct {
	mu     sync.Mutex
	delays []time.Duration
}

func (c *fakeClock) Now() time.Time { return time.Unix(0, 0) }
func (c *fakeClock) Sleep(ctx context.Context, delay time.Duration) error {
	c.mu.Lock()
	c.delays = append(c.delays, delay)
	c.mu.Unlock()
	return nil
}
func (c *fakeClock) AfterFunc(time.Duration, func()) resilience.Timer { return neverTimer{} }

type neverTimer struct{}

func (neverTimer) Stop() bool { return true }

func TestRetryRetriesAndPreservesLastError(t *testing.T) {
	want := errors.New("failed")
	clock := &fakeClock{}
	attempts := 0
	got, err := resilience.Retry(context.Background(), resilience.Policy{
		MaxAttempts: 3,
		Clock:       clock,
		Backoff:     func(int) time.Duration { return time.Second },
	}, func(context.Context) (int, error) {
		attempts++
		return 0, want
	})
	if got != 0 || attempts != 3 || !errors.Is(err, want) {
		t.Fatalf("Retry = (%d, %v), attempts=%d", got, err, attempts)
	}
	var retryErr *resilience.RetryError
	if !errors.As(err, &retryErr) || retryErr.Attempts != 3 {
		t.Fatalf("Retry error = %#v", err)
	}
}

func TestRetryClassifierStopsImmediately(t *testing.T) {
	want := errors.New("stop")
	attempts := 0
	_, err := resilience.Retry(context.Background(), resilience.Policy{
		MaxAttempts: 5,
		Retryable:   func(error) bool { return false },
	}, func(context.Context) (int, error) {
		attempts++
		return 0, want
	})
	if !errors.Is(err, want) || attempts != 1 {
		t.Fatalf("Retry = (%v), attempts=%d", err, attempts)
	}
}

func TestRetryCancellationDuringBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	want := context.Canceled
	_, err := resilience.Retry(ctx, resilience.Policy{
		MaxAttempts: 2,
		Clock:       blockingClock{cancel: cancel},
		Backoff:     func(int) time.Duration { return time.Second },
	}, func(context.Context) (int, error) { return 0, errors.New("failed") })
	if !errors.Is(err, want) {
		t.Fatalf("Retry = %v, want cancellation", err)
	}
}

type blockingClock struct{ cancel context.CancelFunc }

func (blockingClock) Now() time.Time                                   { return time.Time{} }
func (blockingClock) AfterFunc(time.Duration, func()) resilience.Timer { return neverTimer{} }
func (c blockingClock) Sleep(ctx context.Context, _ time.Duration) error {
	c.cancel()
	<-ctx.Done()
	return context.Cause(ctx)
}

func TestExponentialBackoffCapsAndIsRepeatable(t *testing.T) {
	backoff, err := resilience.ExponentialBackoff(time.Second, 100*time.Second, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := backoff(1); got != time.Second || backoff(2) != 2*time.Second || backoff(2) != 2*time.Second || backoff(3) != 4*time.Second || backoff(4) != 8*time.Second {
		t.Fatalf("backoff sequence unexpected")
	}
}

func TestManualClockDrivesRetryDeterministically(t *testing.T) {
	clock := resilience.NewManualClock(time.Unix(0, 0))
	backoff, err := resilience.ExponentialBackoff(time.Second, 8*time.Second, 0)
	if err != nil {
		t.Fatal(err)
	}
	attempts := 0
	_, err = resilience.Retry(context.Background(), resilience.Policy{
		MaxAttempts: 4,
		Backoff:     backoff,
		Clock:       clock,
	}, func(context.Context) (int, error) {
		attempts++
		return 0, errors.New("still failing")
	})
	var re *resilience.RetryError
	if !errors.As(err, &re) || re.Attempts != 4 {
		t.Fatalf("Retry = %v", err)
	}
	// Three backoffs of 1s, 2s, 4s were slept without waiting for any.
	if clock.Sleeps() != 3 || clock.Now().Sub(time.Unix(0, 0)) != 7*time.Second {
		t.Fatalf("sleeps = %d, elapsed = %v", clock.Sleeps(), clock.Now().Sub(time.Unix(0, 0)))
	}

	ctx, cancel := context.WithCancelCause(context.Background())
	cause := errors.New("gave up")
	cancel(cause)
	_, err = resilience.Retry(ctx, resilience.Policy{MaxAttempts: 2, Backoff: backoff, Clock: clock},
		func(context.Context) (int, error) { return 0, errors.New("fail") })
	if !errors.Is(err, cause) {
		t.Fatalf("Retry with done context = %v, want the cause from Sleep", err)
	}
}

func TestManualClockAfterFuncOrderAndStop(t *testing.T) {
	clock := resilience.NewManualClock(time.Unix(0, 0))
	var fired []string
	clock.AfterFunc(3*time.Second, func() { fired = append(fired, "c") })
	clock.AfterFunc(time.Second, func() { fired = append(fired, "a") })
	stopped := clock.AfterFunc(2*time.Second, func() { fired = append(fired, "b") })
	// A callback may schedule more; it runs when its own time comes.
	clock.AfterFunc(time.Second, func() {
		clock.AfterFunc(time.Second, func() { fired = append(fired, "nested") })
	})
	if !stopped.Stop() || stopped.Stop() {
		t.Fatal("Stop should succeed once")
	}
	clock.Advance(10 * time.Second)
	if want := []string{"a", "nested", "c"}; !slices.Equal(fired, want) {
		t.Fatalf("fired = %v, want %v", fired, want)
	}
	// Non-positive delay runs immediately.
	ran := false
	clock.AfterFunc(0, func() { ran = true })
	if !ran {
		t.Fatal("zero-delay AfterFunc did not run")
	}
}
