package async_test

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/apsis-io/velocity/async"
)

func TestErrGroupFirstErrorCancelsSiblings(t *testing.T) {
	eg, ctx := runner(t, async.Unlimited).ErrGroup(context.Background())
	boom := errors.New("boom")
	var stopped atomic.Int32
	for range 4 {
		eg.Go(func(ctx context.Context) error {
			<-ctx.Done()
			stopped.Add(1)
			return nil
		})
	}
	eg.Go(func(context.Context) error { return boom })
	if err := eg.Wait(); !errors.Is(err, boom) {
		t.Fatalf("Wait = %v, want boom", err)
	}
	if stopped.Load() != 4 {
		t.Fatalf("stopped = %d siblings, want 4", stopped.Load())
	}
	if !errors.Is(context.Cause(ctx), boom) {
		t.Fatalf("group context cause = %v", context.Cause(ctx))
	}
	// Wait is repeatable and still reports the first error.
	if err := eg.Wait(); !errors.Is(err, boom) {
		t.Fatalf("second Wait = %v", err)
	}
}

func TestErrGroupWaitIsFirstErrorAndErrorsIsAll(t *testing.T) {
	eg, _ := runner(t, async.Unlimited).ErrGroup(context.Background())
	first, second := errors.New("first"), errors.New("second")
	eg.Go(func(context.Context) error { return first })
	// Fails only after the first failure has cancelled the group, so the
	// order of the two errors is fixed.
	eg.Go(func(ctx context.Context) error { <-ctx.Done(); return second })
	err := eg.Wait()
	if !errors.Is(err, first) || errors.Is(err, second) {
		t.Fatalf("Wait = %v, want first only", err)
	}
	all := eg.Errors()
	if !errors.Is(all, first) || !errors.Is(all, second) {
		t.Fatalf("Errors = %v, want both", all)
	}
	if all.Error() != "first\nsecond" {
		t.Fatalf("Errors order = %q, want submission order", all.Error())
	}
	if eg2, _ := runner(t, async.Unlimited).ErrGroup(context.Background()); eg2.Errors() != nil {
		t.Fatal("Errors on a fresh group is not nil")
	}
}

func TestErrGroupWaitContextBoundsAStraggler(t *testing.T) {
	eg, gctx := runner(t, async.Unlimited).ErrGroup(context.Background())
	release := make(chan struct{})
	eg.Go(func(context.Context) error { <-release; return nil }) // ignores ctx
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := eg.WaitContext(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("WaitContext = %v, want the deadline", err)
	}
	if !errors.Is(context.Cause(gctx), context.DeadlineExceeded) {
		t.Fatalf("group context cause = %v", context.Cause(gctx))
	}
	close(release)
	if err := eg.Wait(); err != nil {
		t.Fatalf("Wait after straggler = %v", err)
	}
}

func TestErrGroupSuccessCancelsContextOnWait(t *testing.T) {
	eg, ctx := runner(t, async.Unlimited).ErrGroup(context.Background())
	var ran atomic.Int32
	for range 3 {
		eg.Go(func(context.Context) error { ran.Add(1); return nil })
	}
	if err := eg.Wait(); err != nil {
		t.Fatal(err)
	}
	if ran.Load() != 3 || !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("ran = %d, ctx = %v", ran.Load(), ctx.Err())
	}
}

// Like Gather, a Limit bounds goroutines: Go blocks the submitter for a
// permit rather than parking one goroutine per function.
func TestErrGroupLimitBoundsGoroutines(t *testing.T) {
	const limit = 4
	eg, _ := runner(t, async.Limited(limit)).ErrGroup(context.Background())
	release := make(chan struct{})
	var running, peak atomic.Int32
	before := runtime.NumGoroutine()
	submitted := make(chan struct{})
	go func() {
		for range 100 {
			eg.Go(func(context.Context) error {
				n := running.Add(1)
				for {
					old := peak.Load()
					if n <= old || peak.CompareAndSwap(old, n) {
						break
					}
				}
				<-release
				running.Add(-1)
				return nil
			})
		}
		close(submitted)
	}()
	time.Sleep(50 * time.Millisecond)
	growth := runtime.NumGoroutine() - before
	close(release)
	<-submitted
	if err := eg.Wait(); err != nil {
		t.Fatal(err)
	}
	if peak.Load() > limit {
		t.Fatalf("peak = %d, want <= %d", peak.Load(), limit)
	}
	if growth > limit+2 {
		t.Fatalf("goroutine growth = %d, want about the limit", growth)
	}
}

func TestErrGroupTryGo(t *testing.T) {
	eg, _ := runner(t, async.Limited(1)).ErrGroup(context.Background())
	release := make(chan struct{})
	if !eg.TryGo(func(context.Context) error { <-release; return nil }) {
		t.Fatal("first TryGo refused")
	}
	if eg.TryGo(func(context.Context) error { return nil }) {
		t.Fatal("TryGo ran past the limit")
	}
	close(release)
	if err := eg.Wait(); err != nil {
		t.Fatal(err)
	}
	// After Wait the context is done; nothing more runs.
	if eg.TryGo(func(context.Context) error { return nil }) {
		t.Fatal("TryGo ran after Wait")
	}
}

func TestErrGroupSubmittedAfterCancelIsNotRun(t *testing.T) {
	eg, _ := runner(t, async.Limited(1)).ErrGroup(context.Background())
	boom := errors.New("boom")
	release := make(chan struct{})
	eg.Go(func(context.Context) error { <-release; return boom })
	ran := false
	done := make(chan struct{})
	go func() {
		// Blocks for the permit; the first function fails before it gets one.
		eg.Go(func(context.Context) error { ran = true; return nil })
		close(done)
	}()
	time.Sleep(10 * time.Millisecond)
	close(release)
	<-done
	if err := eg.Wait(); !errors.Is(err, boom) || ran {
		t.Fatalf("Wait = %v, ran = %t", err, ran)
	}
}

func TestErrGroupRecoversPanicsAsErrors(t *testing.T) {
	eg, _ := runner(t, async.Unlimited).ErrGroup(context.Background())
	eg.Go(func(context.Context) error { panic("callback") })
	err := eg.Wait()
	var p *async.Panic
	if !errors.As(err, &p) || p.Value != "callback" {
		t.Fatalf("Wait = %v, want Panic", err)
	}
}

func TestErrGroupValidation(t *testing.T) {
	eg, _ := runner(t, async.Unlimited).ErrGroup(context.Background())
	eg.Go(nil)
	if err := eg.Wait(); !errors.Is(err, async.ErrNilTask) {
		t.Fatalf("nil fn = %v", err)
	}
	var none *async.Runner
	eg, _ = none.ErrGroup(context.Background())
	eg.Go(func(context.Context) error { return nil })
	if err := eg.Wait(); !errors.Is(err, async.ErrNilRunner) {
		t.Fatalf("nil runner = %v", err)
	}
}

func TestErrGroupHooksSeeEachFunction(t *testing.T) {
	var mu sync.Mutex
	seen := map[int]error{}
	hooks := async.Hooks{OnTaskComplete: func(index int, _ string, _, _ time.Duration, err error) {
		mu.Lock()
		seen[index] = err
		mu.Unlock()
	}}
	eg, _ := runner(t, async.Unlimited, async.WithHooks(hooks)).ErrGroup(context.Background())
	boom := errors.New("boom")
	eg.Go(func(context.Context) error { return nil })
	eg.Go(func(context.Context) error { return boom })
	_ = eg.Wait()
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 2 || seen[0] != nil || !errors.Is(seen[1], boom) {
		t.Fatalf("hooks = %v", seen)
	}
}
