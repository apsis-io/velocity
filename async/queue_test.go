package async_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/apsis-io/velocity/async"
)

func TestQueueFIFOAndClose(t *testing.T) {
	q := async.NewQueue[int](2)
	ctx := context.Background()

	if err := q.Push(ctx, 10); err != nil {
		t.Fatalf("Push(10) = %v", err)
	}

	if err := q.Push(ctx, 20); err != nil {
		t.Fatalf("Push(20) = %v", err)
	}

	// Third push parks until a consumer pops.
	pushFut := q.PushAsync(30)
	if _, ready := pushFut.Try(); ready {
		t.Fatal("expected third push to wait on bounded queue of cap 2")
	}

	v1, ok, err := q.Pop(ctx)
	if err != nil || !ok || v1 != 10 {
		t.Fatalf("Pop 1 = (%d, %v, %v), want (10, true, nil)", v1, ok, err)
	}

	if _, err := pushFut.Await(ctx); err != nil {
		t.Fatalf("pushFut.Await = %v", err)
	}

	q.Close()

	v2, ok, err := q.Pop(ctx)
	if err != nil || !ok || v2 != 20 {
		t.Fatalf("Pop 2 = (%d, %v, %v), want (20, true, nil)", v2, ok, err)
	}

	v3, ok, err := q.Pop(ctx)
	if err != nil || !ok || v3 != 30 {
		t.Fatalf("Pop 3 = (%d, %v, %v), want (30, true, nil)", v3, ok, err)
	}

	_, ok, err = q.Pop(ctx)
	if err != nil || ok {
		t.Fatalf("Pop after drain = (ok=%v, err=%v), want (false, nil)", ok, err)
	}

	if err := q.Push(ctx, 40); !errors.Is(err, async.ErrQueueClosed) {
		t.Fatalf("Push after Close = %v, want ErrQueueClosed", err)
	}
}

func TestQueueCancelledPopNeverDropsItem(t *testing.T) {
	q := async.NewQueue[int](0)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()

	if _, _, err := q.Pop(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Pop timeout = %v, want DeadlineExceeded", err)
	}

	// Now push an item: the timed-out Pop waiter must NOT consume it!
	if err := q.Push(context.Background(), 99); err != nil {
		t.Fatalf("Push(99) = %v", err)
	}

	got, ok, err := q.Pop(context.Background())
	if err != nil || !ok || got != 99 {
		t.Fatalf("Pop after cancelled waiter = (%d, %v, %v), want (99, true, nil)", got, ok, err)
	}
}
