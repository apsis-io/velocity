package ownership

import (
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// netResource is the owned value in these tests: a distinct pointer per
// resource so a drop can name what it was handed.
type netResource struct {
	name string
}

// netCounts records every Drop the tests arm, so an assertion can tell the
// explicit release's drop from the net's.
type netCounts struct {
	drops  atomic.Int32
	closed atomic.Pointer[string]
}

func (n *netCounts) drop(r *netResource) error {
	n.drops.Add(1)
	n.closed.Store(&r.name)

	return nil
}

// settle gives the runtime's cleanup goroutine its chance and keeps garbage
// pressure on while it does: a cleanup fires some time after the object is
// found unreachable, and the find is what the GCs are for.
func settle() {
	for range 10 {
		runtime.GC()
		time.Sleep(time.Millisecond)
	}
}

func TestDropNetRunsDropWhenOwnerUnreachable(t *testing.T) {
	var net netCounts

	// The blank is the point: a handle nobody holds, which is the shape a
	// lost defer leaves behind. The value is unreachable the moment the call
	// ends.
	_, err := New(&netResource{name: "lost"}, WithDrop(net.drop))
	if err != nil {
		t.Fatal(err)
	}

	settle()

	if got := net.drops.Load(); got != 1 {
		t.Fatalf("drops = %d, want the net's one", got)
	}

	if name := *net.closed.Load(); name != "lost" {
		t.Fatalf("net dropped %q, want \"lost\"", name)
	}
}

func TestDropNetHoldsWhileOwnerAlive(t *testing.T) {
	var net netCounts

	owner, err := New(&netResource{name: "held"}, WithDrop(net.drop))
	if err != nil {
		t.Fatal(err)
	}

	settle()

	if got := net.drops.Load(); got != 0 {
		t.Fatalf("drops = %d on a live owner, want none", got)
	}

	runtime.KeepAlive(owner)
}

func TestDropNetDisarmedByRelease(t *testing.T) {
	var net netCounts

	owner, err := New(&netResource{name: "released"}, WithDrop(net.drop))
	if err != nil {
		t.Fatal(err)
	}

	if err := owner.Release(); err != nil {
		t.Fatal(err)
	}

	if got := net.drops.Load(); got != 1 {
		t.Fatalf("drops = %d after Release, want the explicit one", got)
	}

	settle()

	if got := net.drops.Load(); got != 1 {
		t.Fatalf("drops = %d, want the net to stay down after Release", got)
	}
}

func TestDropNetDisarmedByDetach(t *testing.T) {
	var net netCounts

	owner, err := New(&netResource{name: "detached"}, WithDrop(net.drop))
	if err != nil {
		t.Fatal(err)
	}

	value, err := owner.Detach()
	if err != nil {
		t.Fatal(err)
	}

	settle()

	if got := net.drops.Load(); got != 0 {
		t.Fatalf("drops = %d after Detach, want none: the net must not close what it handed over", got)
	}

	runtime.KeepAlive(value)
}

func TestDropNetSurvivesMove(t *testing.T) {
	var net netCounts

	first, err := New(&netResource{name: "moved"}, WithDrop(net.drop))
	if err != nil {
		t.Fatal(err)
	}

	second, err := first.Move()
	if err != nil {
		t.Fatal(err)
	}

	settle()

	if got := net.drops.Load(); got != 0 {
		t.Fatalf("drops = %d with the moved handle alive, want none", got)
	}

	// The keepalive is second's last read; from here the cell is unreachable.
	runtime.KeepAlive(second)

	settle()

	if got := net.drops.Load(); got != 1 {
		t.Fatalf("drops = %d once the last handle went, want the net's one", got)
	}
}

func TestDropNetDropsTheValueTheCellOwnsNow(t *testing.T) {
	var net netCounts

	owner, err := New(&netResource{name: "first"}, WithDrop(net.drop))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := owner.Mutate(func(r **netResource) (struct{}, error) {
		*r = &netResource{name: "second"}

		return struct{}{}, nil
	}); err != nil {
		t.Fatal(err)
	}

	settle()

	if got := net.drops.Load(); got != 1 {
		t.Fatalf("drops = %d, want one", got)
	}

	if name := *net.closed.Load(); name != "second" {
		t.Fatalf("net dropped %q, want \"second\": the stale value would close a resource the cell gave up", name)
	}
}

func TestDropNetAfterMap(t *testing.T) {
	var (
		source  netCounts
		derived netCounts
	)

	owner := NewCloser(onClose(func() error { source.drops.Add(1); return nil }))

	wrapped, err := owner.Map(func(c onClose) (*netResource, error) {
		return &netResource{name: "wrapped"}, nil
	}, WithDrop(derived.drop))
	if err != nil {
		t.Fatal(err)
	}

	settle()

	if got := source.drops.Load(); got != 0 {
		t.Fatalf("source drops = %d with the derived owner alive, want none", got)
	}

	// The keepalive is wrapped's last read; from here its cell is unreachable.
	runtime.KeepAlive(wrapped)

	settle()

	// The derived cell's Drop is the chain: the wrapped value's own drop and
	// the source Close. One run of each, and the source cell's own net stays
	// out of it — otherwise the source would close twice.
	if got := derived.drops.Load(); got != 1 {
		t.Fatalf("derived drops = %d, want the net's one", got)
	}

	if got := source.drops.Load(); got != 1 {
		t.Fatalf("source drops = %d, want the chained one", got)
	}
}

func TestDropNetWaitsForEverySharedHandle(t *testing.T) {
	var net netCounts

	first, err := NewShared(&netResource{name: "shared"}, WithDrop(net.drop))
	if err != nil {
		t.Fatal(err)
	}

	second, err := first.Clone()
	if err != nil {
		t.Fatal(err)
	}

	settle()

	if got := net.drops.Load(); got != 0 {
		t.Fatalf("drops = %d with a clone alive, want none", got)
	}

	// The keepalive is second's last read; from here the cell is unreachable.
	runtime.KeepAlive(second)

	settle()

	if got := net.drops.Load(); got != 1 {
		t.Fatalf("drops = %d once every handle went, want the net's one", got)
	}

	held, err := NewShared(&netResource{name: "released"}, WithDrop(net.drop))
	if err != nil {
		t.Fatal(err)
	}

	if err := held.Release(); err != nil {
		t.Fatal(err)
	}

	settle()

	if got := net.drops.Load(); got != 2 {
		t.Fatalf("drops = %d, want the explicit release's second and no net fire", got)
	}
}

func TestDropNetSingleDropUnderMutation(t *testing.T) {
	var net netCounts

	owner, err := New(&netResource{name: "churning"}, WithDrop(net.drop))
	if err != nil {
		t.Fatal(err)
	}

	// The mutators contend with each other by design — a Mutate refuses while
	// a write borrow is held — so a loser retries, which is the package's own
	// documented pattern for a conflicting Mutate.
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for i := range 200 {
				for {
					_, err := owner.Mutate(func(r **netResource) (struct{}, error) {
						*r = &netResource{name: "churn"}

						return struct{}{}, nil
					})
					if err == nil {
						break
					}

					if !errors.Is(err, ErrConflict) {
						t.Error(err)

						return
					}
				}

				if i%50 == 0 {
					runtime.GC()
				}
			}
		})
	}

	wg.Wait()

	if err := owner.Release(); err != nil {
		t.Fatal(err)
	}

	settle()

	if got := net.drops.Load(); got != 1 {
		t.Fatalf("drops = %d across %d mutations and a release, want exactly one", got, 800)
	}
}

// onClose adapts a func to io.Closer for NewCloser, the periapsis shape this
// net exists for.
type onClose func() error

func (f onClose) Close() error { return f() }

func TestDropNetClosesNewCloserShape(t *testing.T) {
	var closed atomic.Bool

	owner := NewCloser(onClose(func() error { closed.Store(true); return nil }))

	runtime.KeepAlive(owner)

	settle()

	if !closed.Load() {
		t.Fatal("the net never closed the watched fd shape")
	}
}
