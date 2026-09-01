package resilience

import (
	"context"
	"sync"
	"time"
)

// ManualClock is a Clock that moves only when told to, for deterministic
// tests of Retry and Breaker. Sleep advances the clock by the requested
// delay instead of waiting, and honours a context that is already done.
//
//	clock := resilience.NewManualClock(time.Unix(0, 0))
//	breaker, _ := resilience.NewBreaker(resilience.BreakerPolicy{Clock: clock, ...})
//	clock.Advance(policy.OpenFor) // now half-open
//
// It is safe for concurrent use.
type ManualClock struct {
	mu     sync.Mutex
	now    time.Time
	sleeps int
}

// NewManualClock returns a clock reading start.
func NewManualClock(start time.Time) *ManualClock {
	return &ManualClock{now: start}
}

// Now returns the current reading.
func (c *ManualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Sleep advances the clock by d and returns at once. A done context is
// reported instead, as the real clock would, but the clock still advances
// so a test's timeline stays consistent.
func (c *ManualClock) Sleep(ctx context.Context, d time.Duration) error {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.sleeps++
	c.mu.Unlock()
	return context.Cause(ctx)
}

// Advance moves the clock forward by d.
func (c *ManualClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// Set moves the clock to t, which may be earlier than now.
func (c *ManualClock) Set(t time.Time) {
	c.mu.Lock()
	c.now = t
	c.mu.Unlock()
}

// Sleeps reports how many times Sleep has been called, which is how a test
// counts backoff waits without timing anything.
func (c *ManualClock) Sleeps() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sleeps
}
