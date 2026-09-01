package failsafeown_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/apsis-io/velocity/failsafeown"
	"github.com/apsis-io/velocity/ownership"
	failsafe "github.com/failsafe-go/failsafe-go"
	"github.com/failsafe-go/failsafe-go/fallback"
	"github.com/failsafe-go/failsafe-go/hedgepolicy"
	"github.com/failsafe-go/failsafe-go/retrypolicy"
)

// conn stands in for anything whose loss matters: it records that it was
// closed, so a leak is a test failure rather than an inference.
type conn struct{ id int }

type factory struct {
	made   atomic.Int32
	mu     sync.Mutex
	closed []int
}

// dial hands back an owned conn whose Drop records the close.
func (f *factory) dial(context.Context) (*ownership.Owner[*conn], error) {
	c := &conn{id: int(f.made.Add(1))}
	return ownership.New(c, ownership.WithDrop(func(c *conn) error {
		f.mu.Lock()
		f.closed = append(f.closed, c.id)
		f.mu.Unlock()
		return nil
	}))
}

func (f *factory) closedIDs() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int(nil), f.closed...)
}

// waitForClosed polls until n resources have been closed, since a hedge's
// losing attempt is released on its own goroutine after Get has answered.
func (f *factory) waitForClosed(t *testing.T, n int) []int {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if got := f.closedIDs(); len(got) >= n {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("closed %v, want %d resources released", f.closedIDs(), n)
		}
		time.Sleep(time.Millisecond)
	}
}

func owned(t *testing.T, o *ownership.Owner[*conn]) *conn {
	t.Helper()
	c, err := o.View(func(c *conn) (*conn, error) { return c, nil })
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// A retry driven by a result predicate discards a result that arrived
// perfectly well and merely failed the predicate. That is the leak with no
// error anywhere to hint at it.
func TestRetryOnResultReleasesTheRejectedResult(t *testing.T) {
	var f factory
	policy := retrypolicy.NewBuilder[*ownership.Owner[*conn]]().
		HandleIf(func(o *ownership.Owner[*conn], _ error) bool {
			// Reject the first two connections; keep the third.
			c, _ := o.View(func(c *conn) (int, error) { return c.id, nil })
			return c < 3
		}).
		WithMaxAttempts(3).
		Build()

	result, err := failsafeown.Get(context.Background(),
		failsafe.With(policy), f.dial, failsafeown.Hooks[*conn]{})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Release()

	if got := owned(t, result).id; got != 3 {
		t.Fatalf("kept conn %d, want the accepted 3", got)
	}
	// The two rejected connections are closed; the returned one is not.
	closed := f.waitForClosed(t, 2)
	if len(closed) != 2 || closed[0] != 1 || closed[1] != 2 {
		t.Fatalf("closed = %v, want the two rejected connections", closed)
	}
	if state := result.State(); state.Released {
		t.Fatal("the returned connection was released")
	}
}

// A hedge returns the first result and drops the rest, including ones that
// arrive after it has answered.
func TestHedgeReleasesLosingAttempts(t *testing.T) {
	var f factory
	slow := make(chan struct{})
	policy := hedgepolicy.NewWithDelay[*ownership.Owner[*conn]](10 * time.Millisecond)

	var attempts atomic.Int32
	result, err := failsafeown.Get(context.Background(), failsafe.With(policy),
		func(ctx context.Context) (*ownership.Owner[*conn], error) {
			owner, dialErr := f.dial(ctx)
			if attempts.Add(1) == 1 {
				<-slow // the first attempt is overtaken, then returns anyway
			}
			return owner, dialErr
		}, failsafeown.Hooks[*conn]{})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Release()

	close(slow)
	closed := f.waitForClosed(t, 1)
	winner := owned(t, result).id
	for _, id := range closed {
		if id == winner {
			t.Fatalf("the returned connection %d was released", winner)
		}
	}
}

// A fallback returns its own value; the primary's result is dropped.
func TestFallbackReleasesThePrimaryResult(t *testing.T) {
	var f factory
	spare, err := ownership.New(&conn{id: 99}, ownership.WithDrop(func(*conn) error { return nil }))
	if err != nil {
		t.Fatal(err)
	}
	defer spare.Release()

	reject := errors.New("unusable")
	policy := fallback.NewBuilderWithResult(spare).
		HandleIf(func(*ownership.Owner[*conn], error) bool { return true }).
		Build()

	result, err := failsafeown.Get(context.Background(), failsafe.With(policy),
		func(ctx context.Context) (*ownership.Owner[*conn], error) {
			owner, _ := f.dial(ctx)
			return owner, reject // acquired something, then failed
		}, failsafeown.Hooks[*conn]{})
	if err != nil {
		t.Fatal(err)
	}
	if owned(t, result).id != 99 {
		t.Fatalf("result is not the fallback's spare")
	}
	// The primary's connection was acquired and dropped, so it must close.
	if closed := f.waitForClosed(t, 1); closed[0] != 1 {
		t.Fatalf("closed = %v, want the primary's connection", closed)
	}
	if state := spare.State(); state.Released {
		t.Fatal("the fallback's own result was released")
	}
}

// Nothing to discard when the first attempt is the answer.
func TestSuccessReleasesNothing(t *testing.T) {
	var f factory
	policy := retrypolicy.NewWithDefaults[*ownership.Owner[*conn]]()
	result, err := failsafeown.Get(context.Background(), failsafe.With(policy),
		f.dial, failsafeown.Hooks[*conn]{})
	if err != nil {
		t.Fatal(err)
	}
	if got := f.closedIDs(); len(got) != 0 {
		t.Fatalf("closed = %v on the happy path", got)
	}
	if err := result.Release(); err != nil {
		t.Fatal(err)
	}
	if got := f.closedIDs(); len(got) != 1 {
		t.Fatalf("closed = %v after the caller released", got)
	}
}

// Every attempt failing still releases whatever those attempts acquired.
func TestAllAttemptsFailReleasesEverythingAcquired(t *testing.T) {
	var f factory
	boom := errors.New("boom")
	policy := retrypolicy.NewBuilder[*ownership.Owner[*conn]]().WithMaxAttempts(3).Build()

	var discarded atomic.Int32
	result, err := failsafeown.Get(context.Background(), failsafe.With(policy),
		func(ctx context.Context) (*ownership.Owner[*conn], error) {
			owner, _ := f.dial(ctx)
			return owner, boom
		}, failsafeown.Hooks[*conn]{
			OnDiscard: func(*ownership.Owner[*conn], error) { discarded.Add(1) },
		})
	if !errors.Is(err, boom) || result != nil {
		t.Fatalf("Get = (%v, %v), want the failure", result, err)
	}
	if closed := f.waitForClosed(t, 3); len(closed) != 3 {
		t.Fatalf("closed = %v, want all three attempts", closed)
	}
	if got := discarded.Load(); got != 3 {
		t.Fatalf("OnDiscard fired %d times, want 3", got)
	}
}

func TestGetValidation(t *testing.T) {
	exec := failsafe.With(retrypolicy.NewWithDefaults[*ownership.Owner[*conn]]())
	var f factory
	var nilCtx context.Context // a literal nil trips SA1012
	if _, err := failsafeown.Get(nilCtx, exec, f.dial, failsafeown.Hooks[*conn]{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("nil ctx = %v", err)
	}
	if _, err := failsafeown.Get(context.Background(), exec, nil, failsafeown.Hooks[*conn]{}); !errors.Is(err, ownership.ErrNilOption) {
		t.Fatalf("nil fn = %v", err)
	}
	if _, err := failsafeown.Get(context.Background(), nil, f.dial, failsafeown.Hooks[*conn]{}); !errors.Is(err, ownership.ErrNilOption) {
		t.Fatalf("nil executor = %v", err)
	}
}

// The caller's context reaches the executor, so cancelling it ends the
// execution and still releases whatever was acquired.
func TestContextCancellationReleasesAcquired(t *testing.T) {
	var f factory
	ctx, cancel := context.WithCancel(context.Background())
	policy := retrypolicy.NewBuilder[*ownership.Owner[*conn]]().WithMaxAttempts(10).Build()
	boom := errors.New("boom")

	_, err := failsafeown.Get(ctx, failsafe.With(policy),
		func(c context.Context) (*ownership.Owner[*conn], error) {
			owner, _ := f.dial(c)
			cancel()
			return owner, boom
		}, failsafeown.Hooks[*conn]{})
	if err == nil {
		t.Fatal("Get succeeded despite cancellation")
	}
	if closed := f.waitForClosed(t, 1); len(closed) == 0 {
		t.Fatal("the acquired connection was leaked")
	}
}
