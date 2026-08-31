package dedupe_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/apsis-io/velocity/dedupe"
	"github.com/apsis-io/velocity/ownership"
)

func TestHooksObserveLeaderAndFollowerJoins(t *testing.T) {
	type join struct {
		key    string
		leader bool
	}
	var mu sync.Mutex
	joins := make([]join, 0, 2)
	group, err := dedupe.New[string, int](context.Background(), dedupe.WithHooks[string, int](dedupe.Hooks[string]{
		OnJoin: func(key string, leader bool) {
			mu.Lock()
			joins = append(joins, join{key: key, leader: leader})
			mu.Unlock()
		},
	}))
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	fn := func(context.Context) (int, error) {
		select {
		case <-started:
		default:
			close(started)
		}
		<-release
		return 1, nil
	}
	handles := make(chan *ownership.Shared[int], 2)
	for range 2 {
		go func() {
			handle, err := group.Do(context.Background(), "key", fn)
			if err != nil {
				t.Errorf("Do = %v", err)
			}
			handles <- handle
		}()
		<-started
	}
	time.Sleep(time.Millisecond)
	close(release)
	for range 2 {
		if handle := <-handles; handle != nil {
			_ = handle.Release()
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(joins) != 2 || joins[0] != (join{key: "key", leader: true}) || joins[1] != (join{key: "key", leader: false}) {
		t.Fatalf("joins = %+v", joins)
	}
}

func TestHooksOnCompleteCanReenterSameKey(t *testing.T) {
	var group *dedupe.Group[string, int]
	reentered := make(chan error, 1)
	var once sync.Once
	hooks := dedupe.Hooks[string]{
		OnComplete: func(key string, _ time.Duration, _ error) {
			once.Do(func() {
				handle, err := group.Do(context.Background(), key, func(context.Context) (int, error) { return 2, nil })
				if handle != nil {
					_ = handle.Release()
				}
				reentered <- err
			})
		},
	}
	var err error
	group, err = dedupe.New[string, int](context.Background(), dedupe.WithHooks[string, int](hooks))
	if err != nil {
		t.Fatal(err)
	}
	handle, err := group.Do(context.Background(), "key", func(context.Context) (int, error) { return 1, nil })
	if err != nil {
		t.Fatal(err)
	}
	_ = handle.Release()
	select {
	case err := <-reentered:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("same-key reentrant OnComplete deadlocked")
	}
}

// TestHooksObserveCallbackDuration checks OnComplete's duration against an
// independently measured wall-clock bracket around fn itself, not just a
// loose lower bound -- a reported duration that was, say, measured from the
// wrong point (or 10x inflated) would still satisfy "at least as long as we
// slept" but must not satisfy "close to the actual elapsed time."
func TestHooksObserveCallbackDuration(t *testing.T) {
	const leadTime = 40 * time.Millisecond
	const tolerance = 20 * time.Millisecond
	completes := make(chan time.Duration, 1)
	group, err := dedupe.New[string, int](context.Background(), dedupe.WithHooks[string, int](dedupe.Hooks[string]{
		OnComplete: func(_ string, duration time.Duration, _ error) { completes <- duration },
	}))
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	var fnStart, fnEnd time.Time
	fn := func(context.Context) (int, error) {
		fnStart = time.Now()
		close(started)
		<-release
		fnEnd = time.Now()
		return 1, nil
	}
	leader := make(chan *ownership.Shared[int], 1)
	go func() {
		handle, _ := group.Do(context.Background(), "key", fn)
		leader <- handle
	}()
	<-started
	time.Sleep(leadTime)
	// A follower joining mid-flight must not skew the leader's measured
	// duration toward the follower's much shorter wait.
	follower := make(chan *ownership.Shared[int], 1)
	go func() {
		handle, _ := group.Do(context.Background(), "key", fn)
		follower <- handle
	}()
	time.Sleep(time.Millisecond)
	close(release)
	duration := <-completes
	for _, handle := range []*ownership.Shared[int]{<-leader, <-follower} {
		if handle != nil {
			_ = handle.Release()
		}
	}
	wantDuration := fnEnd.Sub(fnStart)
	if delta := duration - wantDuration; delta < 0 || delta > tolerance {
		t.Fatalf("duration = %v, want close to actual fn runtime %v (delta %v, tolerance %v)", duration, wantDuration, delta, tolerance)
	}
	if duration < leadTime {
		t.Fatalf("duration = %v, want at least %v", duration, leadTime)
	}
}
