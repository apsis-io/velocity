package resilience

import (
	"context"
	"slices"
	"sync"
	"time"
)

// ManualClock is a Clock that moves only when told to, for deterministic
// tests of Retry and Breaker. Sleep advances the clock by the requested
// delay instead of waiting, and honours a context that is already done.
// AfterFunc callbacks run synchronously, in due order, on the goroutine
// that advances the clock past them.
//
//	clock := resilience.NewManualClock(time.Unix(0, 0))
//	breaker, _ := resilience.NewBreaker(resilience.BreakerPolicy{Clock: clock, ...})
//	clock.Advance(policy.OpenFor) // now half-open, hook already fired
//
// It is safe for concurrent use.
type ManualClock struct {
	mu     sync.Mutex
	now    time.Time
	sleeps int
	timers []*manualTimer
	nextID uint64
}

type manualTimer struct {
	id      uint64
	due     time.Time
	f       func()
	clock   *ManualClock
	stopped bool
}

// Stop prevents f from running. It reports false if f already ran.
func (t *manualTimer) Stop() bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	if t.stopped {
		return false
	}
	t.stopped = true
	t.clock.timers = slices.DeleteFunc(t.clock.timers, func(p *manualTimer) bool { return p == t })
	return true
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
	c.sleeps++
	c.mu.Unlock()
	c.Advance(d)
	return context.Cause(ctx)
}

// AfterFunc schedules f for when the clock reaches now+d. A non-positive d
// runs f immediately, on this goroutine.
func (c *ManualClock) AfterFunc(d time.Duration, f func()) Timer {
	c.mu.Lock()
	c.nextID++
	t := &manualTimer{id: c.nextID, due: c.now.Add(d), f: f, clock: c}
	c.timers = append(c.timers, t)
	target := c.now
	c.mu.Unlock()
	c.advanceTo(target)
	return t
}

// Advance moves the clock forward by d. Every AfterFunc that comes due runs
// in due order, with the clock reading its due time while it runs, so a
// callback that schedules further work sees the time it was scheduled for
// rather than the end of the jump.
func (c *ManualClock) Advance(d time.Duration) {
	c.mu.Lock()
	target := c.now.Add(d)
	c.mu.Unlock()
	c.advanceTo(target)
}

// Set moves the clock to t, which may be earlier than now. Moving forward
// runs every AfterFunc that comes due, as Advance does.
func (c *ManualClock) Set(t time.Time) {
	c.mu.Lock()
	if t.Before(c.now) {
		c.now = t
		c.mu.Unlock()
		return
	}
	c.mu.Unlock()
	c.advanceTo(t)
}

// advanceTo steps the clock to each due timer in turn, running it outside
// the lock, and finally to target.
func (c *ManualClock) advanceTo(target time.Time) {
	for {
		c.mu.Lock()
		var next *manualTimer
		for _, t := range c.timers {
			if !t.due.After(target) && (next == nil || t.due.Before(next.due) || (t.due.Equal(next.due) && t.id < next.id)) {
				next = t
			}
		}
		if next == nil {
			c.now = target
			c.mu.Unlock()
			return
		}
		if next.due.After(c.now) {
			c.now = next.due
		}
		next.stopped = true
		c.timers = slices.DeleteFunc(c.timers, func(p *manualTimer) bool { return p == next })
		c.mu.Unlock()
		next.f()
	}
}

// Sleeps reports how many times Sleep has been called, which is how a test
// counts backoff waits without timing anything.
func (c *ManualClock) Sleeps() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sleeps
}
