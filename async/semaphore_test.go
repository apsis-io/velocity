package async_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/apsis-io/velocity/async"
)

func TestMutexLockWaitsUnderContext(t *testing.T) {
	mu := async.NewMutex()
	held, err := mu.Lock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if p, ok := mu.TryLock(); ok {
		p.Release()
		t.Fatal("TryLock succeeded while held")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := mu.Lock(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Lock while held = %v, want the deadline", err)
	}

	// A waiter is admitted by the release.
	got := make(chan error, 1)
	go func() {
		p, err := mu.Lock(context.Background())
		if err == nil {
			p.Release()
		}
		got <- err
	}()
	time.Sleep(5 * time.Millisecond)
	held.Release()
	held.Release() // idempotent
	if err := <-got; err != nil {
		t.Fatal(err)
	}
	p, ok := mu.TryLock()
	if !ok {
		t.Fatal("TryLock failed on a free mutex")
	}
	p.Release()
}

func TestSemaphoreAdmitsNAndRejectsDoneContext(t *testing.T) {
	sem, err := async.NewSemaphore(2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := async.NewSemaphore(0); !errors.Is(err, async.ErrInvalidLimit) {
		t.Fatalf("NewSemaphore(0) = %v", err)
	}
	a, _ := sem.Acquire(context.Background())
	b, _ := sem.Acquire(context.Background())
	if p, ok := sem.TryAcquire(); ok {
		p.Release()
		t.Fatal("third permit granted")
	}
	a.Release()
	ctx, cancel := context.WithCancelCause(context.Background())
	cause := errors.New("gone")
	cancel(cause)
	// A free permit does not rescue a caller whose context is already done.
	if _, err := sem.Acquire(ctx); !errors.Is(err, cause) {
		t.Fatalf("Acquire with done ctx = %v, want the cause", err)
	}
	b.Release()

	var inside, peak atomic.Int32
	var wg sync.WaitGroup
	for range 32 {
		wg.Go(func() {
			p, err := sem.Acquire(context.Background())
			if err != nil {
				t.Error(err)
				return
			}
			n := inside.Add(1)
			for {
				old := peak.Load()
				if n <= old || peak.CompareAndSwap(old, n) {
					break
				}
			}
			time.Sleep(200 * time.Microsecond)
			inside.Add(-1)
			p.Release()
		})
	}
	wg.Wait()
	if peak.Load() > 2 {
		t.Fatalf("peak holders = %d, want <= 2", peak.Load())
	}
}

func TestNilPermitAndNilSemaphoreAreSafe(t *testing.T) {
	var p *async.Permit
	p.Release()
	var sem *async.Semaphore
	if _, err := sem.Acquire(context.Background()); !errors.Is(err, async.ErrNilRunner) {
		t.Fatalf("nil semaphore = %v", err)
	}
	if p, ok := sem.TryAcquire(); ok {
		p.Release()
		t.Fatal("nil semaphore granted a permit")
	}
}
