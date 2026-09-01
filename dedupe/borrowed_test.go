package dedupe_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/apsis-io/velocity/dedupe"
	"github.com/apsis-io/velocity/ownership"
)

func TestDoBorrowedSharesLeaderResultAndLeavesFollowerInputUntouched(t *testing.T) {
	group := newGroup(t)
	leaderInput, _ := ownership.New(7)
	followerInput, _ := ownership.New(99)
	started := make(chan struct{})
	release := make(chan struct{})
	leaderResult := make(chan *ownership.Shared[int], 1)
	go func() {
		handle, err := group.DoBorrowed(context.Background(), "key", leaderInput, func(_ context.Context, value int) (int, error) {
			close(started)
			<-release
			return value * 2, nil
		})
		if err != nil {
			t.Errorf("leader = %v", err)
		}
		leaderResult <- handle
	}()
	<-started
	followerResult := make(chan *ownership.Shared[int], 1)
	go func() {
		handle, err := group.DoBorrowed(context.Background(), "key", followerInput, func(context.Context, int) (int, error) {
			t.Error("follower callback ran")
			return 0, nil
		})
		if err != nil {
			t.Errorf("follower = %v", err)
		}
		followerResult <- handle
	}()
	time.Sleep(time.Millisecond)
	close(release)
	for _, handle := range []*ownership.Shared[int]{<-leaderResult, <-followerResult} {
		borrow, err := handle.Borrow()
		if err != nil {
			t.Fatal(err)
		}
		value, err := borrow.Project(func(value int) (int, error) { return value, nil })
		_ = borrow.Release()
		_ = handle.Release()
		if err != nil || value != 14 {
			t.Fatalf("result = (%d, %v)", value, err)
		}
	}
	if state := followerInput.State(); state.Readers != 0 || state.Writer || state.Released || state.Moved {
		t.Fatalf("follower input = %+v", state)
	}
}

func TestDoBorrowedCanceledContextDoesNotAcquire(t *testing.T) {
	group := newGroup(t)
	input, _ := ownership.New(1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	if _, err := group.DoBorrowed(ctx, "key", input, func(context.Context, int) (int, error) {
		called = true
		return 1, nil
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("DoBorrowed = %v", err)
	}
	if called {
		t.Fatal("callback ran for canceled context")
	}
	if state := input.State(); state.Readers != 0 || state.Writer {
		t.Fatalf("input = %+v", state)
	}
}

func TestDoBorrowedCancellationMayOutliveCaller(t *testing.T) {
	completed := make(chan struct{}, 1)
	group, err := dedupe.New[string, int](context.Background(), dedupe.WithHooks[string, int](dedupe.Hooks[string]{
		OnComplete: func(string, time.Duration, error) { completed <- struct{}{} },
	}))
	if err != nil {
		t.Fatal(err)
	}
	input, _ := ownership.New(1)
	started := make(chan struct{})
	release := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := group.DoBorrowed(ctx, "key", input, func(context.Context, int) (int, error) {
			close(started)
			<-release
			return 1, nil
		})
		result <- err
	}()
	<-started
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("DoBorrowed = %v", err)
	}
	if state := input.State(); state.Readers != 1 {
		t.Fatalf("input after caller return = %+v", state)
	}
	close(release)
	select {
	case <-completed:
	case <-time.After(time.Second):
		t.Fatal("OnComplete did not signal loan release")
	}
	if state := input.State(); state.Readers != 0 || state.Writer {
		t.Fatalf("input after completion = %+v", state)
	}
}

func TestDoBorrowedMutPublishesAfterLoanRelease(t *testing.T) {
	group := newGroup(t)
	input, _ := ownership.New(3)
	handle, err := group.DoBorrowedMut(context.Background(), "key", input, func(_ context.Context, value *int) (int, error) {
		*value += 4
		return *value, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if state := input.State(); state.Readers != 0 || state.Writer {
		t.Fatalf("input at publication = %+v", state)
	}
	value, err := input.View(func(value int) (int, error) { return value, nil })
	if err != nil || value != 7 {
		t.Fatalf("input = (%d, %v)", value, err)
	}
	_ = handle.Release()
}

func TestDoBorrowedConflictPrecedesRegistration(t *testing.T) {
	group := newGroup(t)
	input, _ := ownership.New(1)
	write, err := input.BorrowMut()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := group.DoBorrowed(context.Background(), "key", input, func(context.Context, int) (int, error) { return 1, nil }); !errors.Is(err, ownership.ErrConflict) {
		t.Fatalf("DoBorrowed = %v", err)
	}
	if group.Forget("key") {
		t.Fatal("conflicting borrow registered key")
	}
	_ = write.Release()
	handle, err := group.DoBorrowed(context.Background(), "key", input, func(context.Context, int) (int, error) { return 1, nil })
	if err != nil {
		t.Fatal(err)
	}
	_ = handle.Release()
}

func TestDoBorrowedFiresHooks(t *testing.T) {
	var mu sync.Mutex
	joins := make([]bool, 0, 2)
	completes := 0
	completeSignal := make(chan struct{}, 1)
	group, err := dedupe.New[string, int](context.Background(), dedupe.WithHooks[string, int](dedupe.Hooks[string]{
		OnJoin: func(_ string, leader bool) {
			mu.Lock()
			joins = append(joins, leader)
			mu.Unlock()
		},
		OnComplete: func(string, time.Duration, error) {
			mu.Lock()
			completes++
			mu.Unlock()
			select {
			case completeSignal <- struct{}{}:
			default:
			}
		},
	}))
	if err != nil {
		t.Fatal(err)
	}
	leaderInput, _ := ownership.New(1)
	followerInput, _ := ownership.New(2)
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan *ownership.Shared[int], 2)
	go func() {
		handle, _ := group.DoBorrowed(context.Background(), "key", leaderInput, func(context.Context, int) (int, error) {
			close(started)
			<-release
			return 1, nil
		})
		done <- handle
	}()
	<-started
	go func() {
		handle, _ := group.DoBorrowed(context.Background(), "key", followerInput, func(context.Context, int) (int, error) { return 2, nil })
		done <- handle
	}()
	time.Sleep(time.Millisecond)
	close(release)
	for range 2 {
		if handle := <-done; handle != nil {
			_ = handle.Release()
		}
	}
	select {
	case <-completeSignal:
	case <-time.After(time.Second):
		t.Fatal("OnComplete did not fire")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(joins) != 2 || !joins[0] || joins[1] || completes != 1 {
		t.Fatalf("joins=%v completes=%d", joins, completes)
	}
}
