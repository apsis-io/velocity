package resilience

import (
	"context"
	"time"
)

// Clock supplies time to Retry and Breaker: the current reading,
// cancellation-aware sleeping, and deferred execution. ManualClock
// implements it for tests.
type Clock interface {
	Now() time.Time
	Sleep(context.Context, time.Duration) error
	// AfterFunc runs f on its own goroutine once d has elapsed, unless the
	// returned Timer is stopped first.
	AfterFunc(d time.Duration, f func()) Timer
}

// Timer is a pending AfterFunc. Stop reports whether it prevented f from
// running.
type Timer interface {
	Stop() bool
}

type realClock struct{}

// RealClock returns the production wall-clock implementation.
func RealClock() Clock { return realClock{} }

func (realClock) Now() time.Time { return time.Now() }

func (realClock) AfterFunc(d time.Duration, f func()) Timer { return time.AfterFunc(d, f) }

func (realClock) Sleep(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		default:
			return nil
		}
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}
