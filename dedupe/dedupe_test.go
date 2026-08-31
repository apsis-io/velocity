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
	group, err := dedupe.New[string, int](context.Background(), opts...)
	if err != nil {
		t.Fatal(err)
	}
	return group
}

func TestDoDeduplicatesAndClonesHandles(t *testing.T) {
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
	first := make(chan *ownership.Shared[int], 1)
	go func() {
		handle, err := group.Do(context.Background(), "key", fn)
		if err != nil {
			t.Errorf("leader Do = %v", err)
		}
		first <- handle
	}()
	<-started
	secondCh := make(chan *ownership.Shared[int], 1)
	secondErr := make(chan error, 1)
	joined := make(chan struct{})
	go func() {
		close(joined)
		handle, err := group.Do(context.Background(), "key", fn)
		secondCh <- handle
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
	if calls.Load() != 1 {
		t.Fatalf("calls = %d", calls.Load())
	}
	for _, handle := range []*ownership.Shared[int]{one, second} {
		value, err := handle.Snapshot()
		if !errors.Is(err, ownership.ErrNoClone) {
			t.Fatalf("Snapshot = (%d, %v)", value, err)
		}
		if err := handle.Release(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestFreshCallerAfterAbandonmentStartsNewCall(t *testing.T) {
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
		return 1, nil
	}
	leaderCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _, _ = group.Do(leaderCtx, "key", fn) }()
	<-started
	cancel()
	deadline := time.After(time.Second)
	for calls.Load() == 1 {
		freshCtx, freshCancel := context.WithTimeout(context.Background(), time.Millisecond)
		_, _ = group.Do(freshCtx, "key", fn)
		freshCancel()
		select {
		case <-deadline:
			t.Fatal("fresh caller did not start a new generation")
		default:
		}
	}
	close(release)
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
		handle, err := group.Do(context.Background(), "b", func(context.Context) (int, error) {
			t.Error("follower unexpectedly became leader")
			return 0, nil
		})
		if handle != nil {
			_ = handle.Release()
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
	results := <-batchDone
	for _, result := range results {
		if result.Handle != nil {
			_ = result.Handle.Release()
		}
	}
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
	if results["a"].Err != nil || results["a"].Handle == nil {
		t.Fatalf("a = %+v", results["a"])
	}
	if !errors.Is(results["b"].Err, dedupe.ErrMissingResult) || results["b"].Handle != nil {
		t.Fatalf("b = %+v", results["b"])
	}
	_ = results["a"].Handle.Release()
}

func sortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}
