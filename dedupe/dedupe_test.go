package dedupe_test

import (
	"context"
	"errors"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/apsis-io/velocity/dedupe"
	"github.com/apsis-io/velocity/ownership"
)

func newGroup(t *testing.T, opts ...dedupe.Option[string, int]) *dedupe.Group[string, int] {
	t.Helper()
	group, err := dedupe.New[string, int](opts...)
	if err != nil {
		t.Fatal(err)
	}
	return group
}

func TestDoDeduplicatesAndSharesTheValue(t *testing.T) {
	group := newGroup(t)
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	fn := func(context.Context) (int, error) {
		calls.Add(1)
		select {
		case <-started:
		default:
			close(started)
		}
		<-release
		return 7, nil
	}
	first := make(chan int, 1)
	go func() {
		value, err := group.Do(context.Background(), "key", fn)
		if err != nil {
			t.Errorf("leader Do = %v", err)
		}
		first <- value
	}()
	<-started
	secondCh := make(chan int, 1)
	secondErr := make(chan error, 1)
	joined := make(chan struct{})
	go func() {
		close(joined)
		value, err := group.Do(context.Background(), "key", fn)
		secondCh <- value
		secondErr <- err
	}()
	<-joined
	time.Sleep(time.Millisecond)
	close(release)
	one := <-first
	second := <-secondCh
	if err := <-secondErr; err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || one != 7 || second != 7 {
		t.Fatalf("calls = %d, values = %d, %d", calls.Load(), one, second)
	}
}

// An owned group builds one cell per round: every caller's handle counts
// toward it, and Drop runs exactly once, when the last of them releases.
func TestDoSharedOwnedGroupDropsAfterLastRelease(t *testing.T) {
	var drops atomic.Int32
	group := newGroup(t,
		dedupe.WithResultDrop[string](func(int) error { drops.Add(1); return nil }),
		dedupe.WithResultClone[string](func(v int) (int, error) { return v, nil }),
	)
	started := make(chan struct{})
	release := make(chan struct{})
	fn := func(context.Context) (int, error) {
		select {
		case <-started:
		default:
			close(started)
		}
		<-release
		return 7, nil
	}
	handles := make(chan *ownership.Shared[int], 2)
	for range 2 {
		go func() {
			handle, err := group.DoShared(context.Background(), "key", fn)
			if err != nil {
				t.Error(err)
			}
			handles <- handle
		}()
	}
	<-started
	time.Sleep(time.Millisecond)
	close(release)
	one, two := <-handles, <-handles
	if state := one.State(); state.Shares != 2 {
		t.Fatalf("shares = %d, want the two callers' handles", state.Shares)
	}
	if v, err := one.Snapshot(); err != nil || v != 7 {
		t.Fatalf("Snapshot = (%d, %v)", v, err)
	}
	if err := one.Release(); err != nil || drops.Load() != 0 {
		t.Fatalf("first release: err=%v drops=%d", err, drops.Load())
	}
	if err := two.Release(); err != nil || drops.Load() != 1 {
		t.Fatalf("last release: err=%v drops=%d", err, drops.Load())
	}

	// The plain forms are refused rather than letting a copy escape Drop.
	if _, err := group.Do(context.Background(), "key", fn); !errors.Is(err, dedupe.ErrOwnedResult) {
		t.Fatalf("Do on owned group = %v", err)
	}
	results := group.DoBatch(context.Background(), []string{"a"}, func(context.Context, []string) (map[string]int, error) { return nil, nil })
	if !errors.Is(results["a"].Err, dedupe.ErrOwnedResult) {
		t.Fatalf("DoBatch on owned group = %v", results["a"].Err)
	}
	input := ownership.Own(1)
	if _, err := group.DoBorrowed(context.Background(), "key", input, func(context.Context, int) (int, error) { return 0, nil }); !errors.Is(err, dedupe.ErrOwnedResult) {
		t.Fatalf("DoBorrowed on owned group = %v", err)
	}
}

// On a plain group DoShared still works: each caller gets its own cell over
// a copy, with nothing to drop, so the handle API is uniform.
func TestDoSharedPlainGroupGivesIndependentHandles(t *testing.T) {
	group := newGroup(t)
	handle, err := group.DoShared(context.Background(), "key", func(context.Context) (int, error) { return 3, nil })
	if err != nil {
		t.Fatal(err)
	}
	if state := handle.State(); state.Shares != 1 {
		t.Fatalf("shares = %d", state.Shares)
	}
	if v, err := handle.View(func(v int) (int, error) { return v, nil }); err != nil || v != 3 {
		t.Fatalf("View = (%d, %v)", v, err)
	}
	if err := handle.Release(); err != nil {
		t.Fatal(err)
	}
}

// An abandoned round's key stays registered until its callback returns, so
// a callback that ignores its context bounds later callers to one in-flight
// call per key instead of stacking fresh rounds behind it. Forget is the
// explicit way to start over.
func TestAbandonedRoundHoldsKeyUntilCallbackReturns(t *testing.T) {
	group := newGroup(t)
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	fn := func(context.Context) (int, error) { // ignores ctx: blocks on release
		calls.Add(1)
		select {
		case <-started:
		default:
			close(started)
		}
		<-release
		return 1, nil
	}
	leaderCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	leaderDone := make(chan error, 1)
	go func() { _, err := group.Do(leaderCtx, "key", fn); leaderDone <- err }()
	<-started
	cancel()
	if err := <-leaderDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("leader = %v, want cancellation", err)
	}

	// The callback is still running. A fresh caller waits for it rather
	// than starting a second one, and its own deadline bounds that wait.
	freshCtx, freshCancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer freshCancel()
	if _, err := group.Do(freshCtx, "key", fn); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("fresh caller = %v, want its own deadline", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d, want the one still running", calls.Load())
	}

	// A patient caller takes the abandoned callback's value when it finally
	// arrives: the work was done, so it is not repeated.
	patient := make(chan int, 1)
	go func() {
		v, err := group.Do(context.Background(), "key", fn)
		if err != nil {
			t.Error(err)
		}
		patient <- v
	}()
	time.Sleep(10 * time.Millisecond)
	close(release)
	if v := <-patient; v != 1 || calls.Load() != 1 {
		t.Fatalf("patient caller = %d after %d calls, want the abandoned round's value without a second call", v, calls.Load())
	}
}

// Forget releases an abandoned round's key so the next caller starts a new
// round at once instead of waiting for a wedged callback.
func TestForgetReleasesAbandonedKey(t *testing.T) {
	group := newGroup(t)
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	fn := func(context.Context) (int, error) {
		n := calls.Add(1)
		if n == 1 {
			close(started)
			<-release
		}
		return int(n), nil
	}
	leaderCtx, cancel := context.WithCancel(context.Background())
	leaderDone := make(chan struct{})
	go func() { _, _ = group.Do(leaderCtx, "key", fn); close(leaderDone) }()
	<-started
	cancel()
	<-leaderDone
	if !group.Forget("key") {
		t.Fatal("Forget = false")
	}
	value, err := group.Do(context.Background(), "key", fn)
	if err != nil || value != 2 {
		t.Fatalf("caller after Forget = (%d, %v), want a second round", value, err)
	}
	close(release)
}

// When the abandoned callback honours its context it returns promptly, and
// the waiting caller then leads a fresh round.
func TestFreshCallerFollowsCooperativeAbandonedRound(t *testing.T) {
	group := newGroup(t)
	started := make(chan struct{})
	var calls atomic.Int32
	fn := func(ctx context.Context) (int, error) {
		n := calls.Add(1)
		if n == 1 {
			close(started)
			<-ctx.Done()
			return 0, ctx.Err()
		}
		return int(n), nil
	}
	leaderCtx, cancel := context.WithCancel(context.Background())
	leaderDone := make(chan struct{})
	go func() { _, _ = group.Do(leaderCtx, "key", fn); close(leaderDone) }()
	<-started
	cancel()
	<-leaderDone // the round is abandoned only once its last caller has left
	value, err := group.Do(context.Background(), "key", fn)
	if err != nil || value != 2 {
		t.Fatalf("fresh caller = (%d, %v), want a second round's value", value, err)
	}
}

func TestForgetAndCancel(t *testing.T) {
	group := newGroup(t)
	started := make(chan struct{})
	release := make(chan struct{})
	fn := func(ctx context.Context) (int, error) {
		close(started)
		<-release
		return 1, ctx.Err()
	}
	go func() { _, _ = group.Do(context.Background(), "key", fn) }()
	<-started
	if !group.Forget("key") {
		t.Fatal("Forget = false")
	}
	if group.Cancel("key") {
		t.Fatal("Cancel found forgotten key")
	}
	close(release)
}

func TestDoBatchCancellationWaitsForAllLeaderKeys(t *testing.T) {
	group := newGroup(t)
	started := make(chan struct{})
	contextDone := make(chan struct{})
	release := make(chan struct{})
	batchDone := make(chan map[string]dedupe.Result[int], 1)
	batchCtx, cancelBatch := context.WithCancel(context.Background())
	go func() {
		batchDone <- group.DoBatch(batchCtx, []string{"a", "b"}, func(ctx context.Context, keys []string) (map[string]int, error) {
			close(started)
			go func() { <-ctx.Done(); close(contextDone) }()
			<-release
			return map[string]int{"a": 1, "b": 2}, nil
		})
	}()
	<-started

	followerDone := make(chan error, 1)
	go func() {
		value, err := group.Do(context.Background(), "b", func(context.Context) (int, error) {
			t.Error("follower unexpectedly became leader")
			return 0, nil
		})
		if err == nil && value != 2 {
			t.Errorf("follower value = %d, want 2", value)
		}
		followerDone <- err
	}()
	time.Sleep(time.Millisecond)
	cancelBatch()
	select {
	case <-contextDone:
		t.Fatal("batch context canceled while key b still had a waiter")
	case <-time.After(10 * time.Millisecond):
	}
	close(release)
	<-batchDone
	if err := <-followerDone; err != nil {
		t.Fatal(err)
	}
}

func TestDoBatchAlignedMissingErrors(t *testing.T) {
	group := newGroup(t)
	results := group.DoBatch(context.Background(), []string{"a", "b", "a"}, func(context.Context, []string) (map[string]int, error) {
		return map[string]int{"a": 1}, nil
	})
	if !slices.Equal([]string{"a", "b"}, sortedKeys(results)) {
		t.Fatalf("keys = %v", sortedKeys(results))
	}
	if results["a"].Err != nil || results["a"].Value != 1 {
		t.Fatalf("a = %+v", results["a"])
	}
	if !errors.Is(results["b"].Err, dedupe.ErrMissingResult) || results["b"].Value != 0 {
		t.Fatalf("b = %+v", results["b"])
	}
}

func sortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

// A zero-value Group works with every default, as singleflight.Group does,
// so a Group can be a plain struct field.
func TestZeroValueGroupIsUsable(t *testing.T) {
	type manager struct {
		pulls dedupe.Group[string, int]
	}
	m := &manager{}
	value, err := m.pulls.Do(context.Background(), "img", func(context.Context) (int, error) { return 3, nil })
	if err != nil || value != 3 {
		t.Fatalf("Do = (%d, %v)", value, err)
	}
	if m.pulls.Forget("img") {
		t.Fatal("Forget found a finished key")
	}
	results := m.pulls.DoBatch(context.Background(), []string{"a"}, func(context.Context, []string) (map[string]int, error) {
		return map[string]int{"a": 1}, nil
	})
	if results["a"].Value != 1 {
		t.Fatalf("DoBatch = %+v", results)
	}
}

func TestMustPanicsOnlyOnError(t *testing.T) {
	group := dedupe.Must(dedupe.New[string, int]())
	if group == nil {
		t.Fatal("Must returned nil")
	}
	defer func() {
		if recover() == nil {
			t.Fatal("Must did not panic on error")
		}
	}()
	dedupe.Must(dedupe.New[string, int](dedupe.WithSharded[string, int](0)))
}
