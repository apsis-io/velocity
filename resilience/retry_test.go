package resilience_test

import (
	"context"
	"errors"
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

func (blockingClock) Now() time.Time { return time.Time{} }
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
