package failsafeown_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
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

// artifact is a temp directory with a blob inside it: the case where a
// leak is silent and permanent, with no pool or GC to bound it.
type artifact struct {
	dir  string
	blob string
	from string // which strategy produced it
}

// fetch creates the artifact on disk and owns it, so Drop is the removal.
func fetch(root, from string) (*ownership.Owner[*artifact], error) {
	dir, err := os.MkdirTemp(root, from+"-")
	if err != nil {
		return nil, err
	}
	blob := filepath.Join(dir, "layer.tar")
	if err := os.WriteFile(blob, []byte(from), 0o600); err != nil {
		return nil, err
	}
	a := &artifact{dir: dir, blob: blob, from: from}
	return ownership.New(a, ownership.WithDrop(func(a *artifact) error {
		return os.RemoveAll(a.dir)
	}))
}

// waitFor polls until cond holds, for assertions about attempts rather
// than about the result. A hedge's losing attempt outlives the call by
// design, so reading straight after Get sees whatever happened to land.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// A hedge whose attempts differ needs to know which one it is. Racing a
// peer against a registry is the shape the package doc leads with, and it
// cannot be written at all without the Execution handle: with only Get,
// every attempt is byte-identical.
func TestGetWithExecutionDispatchesByAttemptAndDisposesTheLoser(t *testing.T) {
	root := t.TempDir()
	slow := make(chan struct{})
	var dispatched sync.Map // strategy -> struct{}

	// A zero delay is a plain race, which is what this shape wants and also
	// the case that exposes the Hedges() trap: with a long delay the primary
	// reads the counter before the hedge exists, so a wrong discriminator
	// would still pass here.
	policy := hedgepolicy.NewBuilderWithDelay[*ownership.Owner[*artifact]](0).
		CancelIf(func(_ *ownership.Owner[*artifact], err error) bool { return err == nil }).
		Build()
	result, err := failsafeown.GetWithExecution(context.Background(), failsafe.With(policy),
		func(e failsafe.Execution[*ownership.Owner[*artifact]]) (*ownership.Owner[*artifact], error) {
			// IsHedge is a per-attempt bool. Hedges() is a shared count and
			// would send both arms down the same branch here.
			if !e.IsHedge() {
				dispatched.Store("peer", struct{}{})
				owner, ferr := fetch(root, "peer")
				// The peer accepted, then stalled. Bounded, so that a
				// dispatch bug — both attempts taking this branch — fails
				// the assertion below rather than deadlocking the test.
				select {
				case <-slow:
				case <-time.After(2 * time.Second):
				}
				return owner, ferr
			}
			dispatched.Store("registry", struct{}{})
			return fetch(root, "registry")
		}, failsafeown.Hooks[*artifact]{})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Release()

	waitFor(t, "both arms to dispatch", func() bool {
		_, peer := dispatched.Load("peer")
		_, registry := dispatched.Load("registry")
		return peer && registry
	})
	if _, ok := dispatched.Load("registry"); !ok {
		t.Fatal("the hedge never dispatched to the registry: attempts were interchangeable")
	}
	winner, err := result.View(func(a *artifact) (artifact, error) { return *a, nil })
	if err != nil {
		t.Fatal(err)
	}
	if winner.from != "registry" {
		t.Fatalf("winner came from %q, want the fast registry", winner.from)
	}

	// The peer's artifact arrives late and must be removed from disk.
	close(slow)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		entries, readErr := os.ReadDir(root)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if len(entries) == 1 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !exists(winner.blob) {
		t.Fatalf("the returned artifact %s was removed", winner.blob)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || !strings.HasPrefix(entries[0].Name(), "registry-") {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Fatalf("artifact directories remaining = %v, want only the winner's", names)
	}
}

// Retries reports failures rather than hedges, so a chain with both still
// tells fn which branch it is on — the thing a closure counter cannot.
func TestGetWithExecutionSeesRetriesSeparatelyFromHedges(t *testing.T) {
	boom := errors.New("boom")
	var mu sync.Mutex
	var seen []string
	policy := retrypolicy.NewBuilder[*ownership.Owner[*conn]]().WithMaxAttempts(3).Build()

	var f factory
	result, err := failsafeown.GetWithExecution(context.Background(), failsafe.With(policy),
		func(e failsafe.Execution[*ownership.Owner[*conn]]) (*ownership.Owner[*conn], error) {
			mu.Lock()
			seen = append(seen, fmt.Sprintf("retry=%d first=%t", e.Retries(), e.IsFirstAttempt()))
			retries := e.Retries()
			mu.Unlock()
			owner, _ := f.dial(e.Context())
			if retries < 2 {
				return owner, boom
			}
			return owner, nil
		}, failsafeown.Hooks[*conn]{})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Release()

	mu.Lock()
	defer mu.Unlock()
	want := []string{"retry=0 first=true", "retry=1 first=false", "retry=2 first=false"}
	if !slices.Equal(seen, want) {
		t.Fatalf("attempts saw %v, want %v", seen, want)
	}
	// The two failed attempts each dialled and each must be closed.
	if closed := f.waitForClosed(t, 2); len(closed) != 2 {
		t.Fatalf("closed = %v, want the two failed attempts", closed)
	}
}

func TestGetWithExecutionValidation(t *testing.T) {
	exec := failsafe.With(retrypolicy.NewWithDefaults[*ownership.Owner[*conn]]())
	fn := func(failsafe.Execution[*ownership.Owner[*conn]]) (*ownership.Owner[*conn], error) {
		return nil, nil
	}
	var nilCtx context.Context // a literal nil trips SA1012
	if _, err := failsafeown.GetWithExecution(nilCtx, exec, fn, failsafeown.Hooks[*conn]{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("nil ctx = %v", err)
	}
	if _, err := failsafeown.GetWithExecution(context.Background(), exec, nil, failsafeown.Hooks[*conn]{}); !errors.Is(err, ownership.ErrNilOption) {
		t.Fatalf("nil fn = %v", err)
	}
	if _, err := failsafeown.GetWithExecution(context.Background(), nil, fn, failsafeown.Hooks[*conn]{}); !errors.Is(err, ownership.ErrNilOption) {
		t.Fatalf("nil executor = %v", err)
	}
}

// Get hands fn the execution's context, so a policy that gives up on an
// attempt stops it rather than letting it run on.
func TestGetPassesTheExecutionContext(t *testing.T) {
	var f factory
	stopped := make(chan struct{})
	policy := hedgepolicy.NewWithDelay[*ownership.Owner[*conn]](5 * time.Millisecond)
	var attempts atomic.Int32

	result, err := failsafeown.Get(context.Background(), failsafe.With(policy),
		func(ctx context.Context) (*ownership.Owner[*conn], error) {
			owner, dialErr := f.dial(ctx)
			if attempts.Add(1) == 1 {
				select {
				case <-ctx.Done(): // cancelled once the hedge wins
					close(stopped)
				case <-time.After(2 * time.Second):
				}
			}
			return owner, dialErr
		}, failsafeown.Hooks[*conn]{})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Release()
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("the losing attempt's context was never cancelled")
	}
}

// Hedges() counts hedges that exist, including in-progress ones, and is
// shared by every attempt; IsHedge() is per-attempt. Using the former to
// tell the arms apart sends both down one branch, so the other never runs —
// a hang rather than a wrong answer. This pins the distinction so the doc's
// claim is executable rather than remembered.
func TestHedgesIsSharedAndIsHedgeIsPerAttempt(t *testing.T) {
	var arrived atomic.Int32
	var once sync.Once
	proceed := make(chan struct{})
	var mu sync.Mutex
	var hedgeCounts []int
	var isHedgeFlags []bool

	policy := hedgepolicy.NewBuilderWithDelay[*ownership.Owner[*conn]](0).
		CancelIf(func(_ *ownership.Owner[*conn], err error) bool { return err == nil }).
		Build()

	var f factory
	result, err := failsafeown.GetWithExecution(context.Background(), failsafe.With(policy),
		func(e failsafe.Execution[*ownership.Owner[*conn]]) (*ownership.Owner[*conn], error) {
			// Wait until both attempts exist, so the shared counter is read
			// at a point where its value is settled rather than raced. The
			// close is guarded: both attempts can reach the threshold check.
			if arrived.Add(1) == 2 {
				once.Do(func() { close(proceed) })
			}
			select {
			case <-proceed:
			case <-time.After(2 * time.Second):
			}
			mu.Lock()
			hedgeCounts = append(hedgeCounts, e.Hedges())
			isHedgeFlags = append(isHedgeFlags, e.IsHedge())
			mu.Unlock()
			return f.dial(e.Context())
		}, failsafeown.Hooks[*conn]{})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Release()

	waitFor(t, "both attempts to record", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(isHedgeFlags) == 2
	})

	mu.Lock()
	defer mu.Unlock()
	if len(isHedgeFlags) != 2 {
		t.Fatalf("attempts = %d, want the primary and one hedge", len(isHedgeFlags))
	}
	// Exactly one attempt is the hedge: a usable discriminator.
	trues := 0
	for _, isHedge := range isHedgeFlags {
		if isHedge {
			trues++
		}
	}
	if trues != 1 {
		t.Fatalf("IsHedge = %v, want exactly one true", isHedgeFlags)
	}
	// Both read the same shared count, which is why it cannot discriminate.
	if hedgeCounts[0] != hedgeCounts[1] {
		t.Fatalf("Hedges() = %v; the counter is documented as shared, so both attempts should read alike once both exist", hedgeCounts)
	}
}
