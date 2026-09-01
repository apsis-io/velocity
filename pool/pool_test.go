package pool_test

import (
	"context"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/apsis-io/velocity/ownership"
	"github.com/apsis-io/velocity/pool"
)

// conn is a resource with an identity, so tests can tell reuse from
// construction and see which ones were closed.
type conn struct{ id int }

type factory struct {
	mu       sync.Mutex
	made     int
	closed   []int
	newErr   error
	closeErr error
}

func (f *factory) config(max int) pool.Config[*conn] {
	return pool.Config[*conn]{
		New: func(context.Context) (*conn, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.newErr != nil {
				return nil, f.newErr
			}
			f.made++
			return &conn{id: f.made}, nil
		},
		Close: func(c *conn) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.closed = append(f.closed, c.id)
			return f.closeErr
		},
		Max: max,
	}
}

func (f *factory) counts() (made int, closed []int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.made, slices.Clone(f.closed)
}

func newPool(t *testing.T, cfg pool.Config[*conn]) *pool.Pool[*conn] {
	t.Helper()
	p, err := pool.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestNewValidation(t *testing.T) {
	tests := []struct {
		name string
		cfg  pool.Config[int]
		want error
	}{
		{"nil New", pool.Config[int]{Max: 1}, ownership.ErrNilOption},
		{"zero Max", pool.Config[int]{New: func(context.Context) (int, error) { return 0, nil }}, pool.ErrInvalidMax},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := pool.New(tt.cfg)
			var ce *pool.ConfigError
			if !errors.Is(err, tt.want) || !errors.Is(err, pool.ErrInvalidConfig) || !errors.As(err, &ce) {
				t.Fatalf("error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestGetReusesMostRecentlyReturned(t *testing.T) {
	var f factory
	p := newPool(t, f.config(4))
	defer p.Close()
	ctx := context.Background()

	first, err := p.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	second, err := p.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Stats(); got != (pool.Stats{InUse: 2, Max: 4}) {
		t.Fatalf("stats = %+v", got)
	}
	// Return first then second: second is on top and is reused next.
	if err := first.Release(); err != nil {
		t.Fatal(err)
	}
	if err := second.Release(); err != nil {
		t.Fatal(err)
	}
	if got := p.Stats(); got != (pool.Stats{Idle: 2, Max: 4}) {
		t.Fatalf("stats = %+v", got)
	}
	third, err := p.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer third.Release()
	value, err := third.Value()
	if err != nil || value.id != 2 {
		t.Fatalf("reused = (%+v, %v), want conn 2", value, err)
	}
	if made, _ := f.counts(); made != 2 {
		t.Fatalf("made = %d, want 2", made)
	}
}

func TestCheckoutIsALease(t *testing.T) {
	var f factory
	p := newPool(t, f.config(1))
	defer p.Close()

	c, err := p.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Release(); err != nil {
		t.Fatal(err)
	}
	// Use after return is caught, and a second return is a no-op.
	if _, err := c.Value(); !errors.Is(err, ownership.ErrReleased) {
		t.Fatalf("Value after Release = %v", err)
	}
	if err := c.Release(); err != nil {
		t.Fatalf("second Release = %v", err)
	}
	if got := p.Stats(); got != (pool.Stats{Idle: 1, Max: 1}) {
		t.Fatalf("double return changed stats: %+v", got)
	}
	// Discard after Release does not close a resource now owned by the pool.
	if err := c.Discard(); err != nil {
		t.Fatal(err)
	}
	if _, closed := f.counts(); len(closed) != 0 {
		t.Fatalf("closed = %v after late Discard", closed)
	}
}

func TestDiscardClosesAndFreesCapacity(t *testing.T) {
	var f factory
	p := newPool(t, f.config(1))
	defer p.Close()
	ctx := context.Background()

	c, err := p.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Discard(); err != nil {
		t.Fatal(err)
	}
	if _, closed := f.counts(); !slices.Equal(closed, []int{1}) {
		t.Fatalf("closed = %v, want [1]", closed)
	}
	if got := p.Stats(); got != (pool.Stats{Max: 1}) {
		t.Fatalf("stats after discard = %+v", got)
	}
	// The capacity is free again and a fresh resource is made.
	next, err := p.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Release()
	if value, _ := next.Value(); value.id != 2 {
		t.Fatalf("after discard got conn %d, want a new one", value.id)
	}
}

func TestMoveKeepsDiscard(t *testing.T) {
	var f factory
	p := newPool(t, f.config(1))
	defer p.Close()

	c, err := p.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	moved, err := c.Move()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Value(); !errors.Is(err, ownership.ErrReleased) {
		t.Fatalf("original after Move = %v", err)
	}
	if err := moved.Discard(); err != nil {
		t.Fatal(err)
	}
	if _, closed := f.counts(); !slices.Equal(closed, []int{1}) {
		t.Fatalf("closed = %v, want [1]", closed)
	}
}

func TestGetWaitsForCapacityAndHonoursContext(t *testing.T) {
	var f factory
	p := newPool(t, f.config(1))
	defer p.Close()
	ctx := context.Background()

	held, err := p.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}

	timeout, cancel := context.WithTimeout(ctx, 10*time.Millisecond)
	defer cancel()
	if _, err := p.Get(timeout); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Get at capacity = %v, want deadline", err)
	}

	// A waiter is admitted by the release, and gets the returned resource.
	got := make(chan *pool.Checkout[*conn], 1)
	go func() {
		c, err := p.Get(ctx)
		if err != nil {
			t.Error(err)
		}
		got <- c
	}()
	time.Sleep(5 * time.Millisecond)
	if err := held.Release(); err != nil {
		t.Fatal(err)
	}
	c := <-got
	defer c.Release()
	if value, _ := c.Value(); value.id != 1 {
		t.Fatalf("waiter got conn %d, want the released conn 1", value.id)
	}
	if made, _ := f.counts(); made != 1 {
		t.Fatalf("made = %d, want 1", made)
	}
}

func TestMaxBoundsResourcesUnderContention(t *testing.T) {
	const max = 4
	var f factory
	p := newPool(t, f.config(max))
	defer p.Close()
	ctx := context.Background()

	var inUse, peak atomic.Int32
	var wg sync.WaitGroup
	for range 64 {
		wg.Go(func() {
			c, err := p.Get(ctx)
			if err != nil {
				t.Error(err)
				return
			}
			n := inUse.Add(1)
			for {
				old := peak.Load()
				if n <= old || peak.CompareAndSwap(old, n) {
					break
				}
			}
			time.Sleep(200 * time.Microsecond)
			inUse.Add(-1)
			if n%7 == 0 {
				_ = c.Discard()
			} else {
				_ = c.Release()
			}
		})
	}
	wg.Wait()
	if peak.Load() > max {
		t.Fatalf("peak in use = %d, want <= %d", peak.Load(), max)
	}
	stats := p.Stats()
	if stats.InUse != 0 || stats.Idle > max {
		t.Fatalf("stats = %+v", stats)
	}
	made, closed := f.counts()
	if made-len(closed) != stats.Idle {
		t.Fatalf("made %d, closed %d, idle %d: resources leaked or double-counted", made, len(closed), stats.Idle)
	}
}

func TestNewErrorReturnsCapacity(t *testing.T) {
	var f factory
	f.newErr = errors.New("dial failed")
	p := newPool(t, f.config(1))
	defer p.Close()
	ctx := context.Background()

	if _, err := p.Get(ctx); !errors.Is(err, f.newErr) {
		t.Fatalf("Get = %v", err)
	}
	// Had the permit leaked, this Get would block on a Max of one.
	f.mu.Lock()
	f.newErr = nil
	f.mu.Unlock()
	timeout, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	c, err := p.Get(timeout)
	if err != nil {
		t.Fatalf("Get after failed New = %v", err)
	}
	_ = c.Release()
}

func TestCloseDestroysIdleAndOutstandingOnReturn(t *testing.T) {
	var f factory
	p := newPool(t, f.config(2))
	ctx := context.Background()

	held, err := p.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	idle, err := p.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_ = idle.Release()

	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if _, closed := f.counts(); !slices.Equal(closed, []int{2}) {
		t.Fatalf("closed at Close = %v, want the idle conn 2", closed)
	}
	if _, err := p.Get(ctx); !errors.Is(err, pool.ErrClosed) {
		t.Fatalf("Get after Close = %v", err)
	}
	// The outstanding checkout is still usable and is destroyed on return.
	if _, err := held.Value(); err != nil {
		t.Fatalf("held after Close = %v", err)
	}
	if err := held.Release(); err != nil {
		t.Fatal(err)
	}
	if _, closed := f.counts(); !slices.Equal(closed, []int{2, 1}) {
		t.Fatalf("closed after return = %v, want [2 1]", closed)
	}
	if got := p.Stats(); got != (pool.Stats{Max: 2}) {
		t.Fatalf("stats after teardown = %+v", got)
	}
	if err := p.Close(); err != nil {
		t.Fatalf("second Close = %v", err)
	}
}

func TestCloseJoinsErrorsAndReleaseReportsThem(t *testing.T) {
	var f factory
	f.closeErr = errors.New("close failed")
	p := newPool(t, f.config(2))
	ctx := context.Background()

	a, _ := p.Get(ctx)
	b, _ := p.Get(ctx)
	_ = a.Release()
	if err := p.Close(); !errors.Is(err, f.closeErr) {
		t.Fatalf("Close = %v", err)
	}
	if err := b.Release(); !errors.Is(err, f.closeErr) {
		t.Fatalf("Release after Close = %v", err)
	}
	if err := b.Release(); !errors.Is(err, f.closeErr) {
		t.Fatalf("repeat Release = %v, want the first error", err)
	}
}

func TestHooksReportWaitCreationAndDiscard(t *testing.T) {
	var f factory
	cfg := f.config(1)
	var mu sync.Mutex
	type acquire struct {
		waited  time.Duration
		created bool
		err     error
	}
	var acquires []acquire
	var releases []bool
	cfg.Hooks = pool.Hooks{
		OnAcquire: func(waited time.Duration, created bool, err error) {
			mu.Lock()
			acquires = append(acquires, acquire{waited, created, err})
			mu.Unlock()
		},
		OnRelease: func(discarded bool, _ error) {
			mu.Lock()
			releases = append(releases, discarded)
			mu.Unlock()
		},
	}
	p := newPool(t, cfg)
	defer p.Close()
	ctx := context.Background()

	first, _ := p.Get(ctx)
	waiterDone := make(chan struct{})
	go func() {
		c, _ := p.Get(ctx)
		_ = c.Discard()
		close(waiterDone)
	}()
	time.Sleep(20 * time.Millisecond)
	_ = first.Release()
	<-waiterDone

	mu.Lock()
	defer mu.Unlock()
	if len(acquires) != 2 || !acquires[0].created || acquires[1].created {
		t.Fatalf("acquires = %+v", acquires)
	}
	if acquires[1].waited < 15*time.Millisecond {
		t.Fatalf("waiter reported %v, want the ~20ms it queued", acquires[1].waited)
	}
	if !slices.Equal(releases, []bool{false, true}) {
		t.Fatalf("releases = %v, want return then discard", releases)
	}
}

// A Checkout is an io.Closer through its Lease, so a Scope can unwind it
// with everything else acquired during a construction.
func TestCheckoutInScope(t *testing.T) {
	var f factory
	p := newPool(t, f.config(1))
	defer p.Close()

	scope := ownership.NewScope()
	c, err := p.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := scope.OwnCloser(c); err != nil {
		t.Fatal(err)
	}
	if err := scope.Close(); err != nil {
		t.Fatal(err)
	}
	if got := p.Stats(); got != (pool.Stats{Idle: 1, Max: 1}) {
		t.Fatalf("stats after scope unwind = %+v", got)
	}
}
