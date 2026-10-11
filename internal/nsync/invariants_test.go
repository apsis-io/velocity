package nsync

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
	"unsafe"
)

func TestLibraryDoesNotUseChannels(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}

	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}

		fset := token.NewFileSet()

		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}

		ast.Inspect(file, func(node ast.Node) bool {
			switch node := node.(type) {
			case *ast.ChanType, *ast.SendStmt:
				t.Errorf("channel operation in library: %s", fset.Position(node.Pos()))
			case *ast.UnaryExpr:
				if node.Op == token.ARROW {
					t.Errorf("channel receive in library: %s", fset.Position(node.Pos()))
				}
			}

			return true
		})
	}
}

func TestCacheLineLayouts(t *testing.T) {
	// The inline Mutex is padded to its own cache line: it is embedded
	// beside the fields it guards, and a shared line would make the lock
	// word and those fields ping-pong across cores — measured at ~5% of
	// parallel acquire before the pad, against the pointer-based form.
	var m Mutex
	if size := unsafe.Sizeof(m); size != cacheLineSize {
		t.Errorf("Mutex size %d is not one cache line (%d)", size, cacheLineSize)
	}
}

func TestDisarmWaitsForStartedCallback(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var q waitQueue

		w := getWaiter(&q.mu)
		q.mu.Lock()
		q.pushBack(w)

		started := make(chan struct{})
		timer := time.AfterFunc(0, func() { close(started); w.fire() })

		<-started
		// The callback has started but cannot cancel the waiter while q.mu is
		// held. disarm must release q.mu while waiting for it, then reacquire it.
		w.disarm(waitCancel{timer: timer})

		if q.mu.TryLock() {
			t.Fatal("disarm returned without the queue lock held")
		}
		if !w.canceled || w.queued || q.head != nil {
			t.Fatal("the callback did not remove the waiter")
		}
		q.mu.Unlock()
		putWaiter(w)
	})
}

func TestStarvingMutexHandsOffInOrder(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := NewTryMutex()
		m.Lock()
		entered := make(chan int)
		release := make(chan struct{})

		var workers sync.WaitGroup
		for i := range 8 {
			workers.Go(func() {
				m.Lock()
				entered <- i

				<-release
				m.Unlock()
			})
			synctest.Wait() // queue the waiters in a known order
		}
		// Force handoff mode deterministically, without depending on the host
		// scheduler to exceed the handoff threshold.
		m.state.state.Or(stateStarving)
		m.Unlock()

		if m.TryLock() {
			m.Unlock()
			t.Fatal("a new arrival took a lock handed to a waiter")
		}
		for want := range 8 {
			if got := <-entered; got != want {
				t.Errorf("waiter %d acquired the lock in position %d", got, want)
			}

			release <- struct{}{}
		}

		workers.Wait()

		if s := m.state.state.Load(); s != 0 {
			t.Fatalf("state after all waiters left = %#x, want 0", s)
		}
	})
}

func TestReleaseClearsQueuedFlagOfEmptyQueue(t *testing.T) {
	// A canceled waiter's callback removes it from the queue before the
	// waiter clears stateQueued. A release in between must clear it.
	var m lockState
	m.lock()
	m.waitQueue()
	m.state.Store(stateUnit | stateQueued)
	m.unlock()

	if s := m.state.Load(); s != 0 {
		t.Fatalf("state = %#x, want 0", s)
	}

	if !m.tryLock() {
		t.Fatal("release left the mutex unusable")
	}

	m.unlock()
}

func TestLastCanceledWaiterClearsHandoff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var m lockState
		m.lock()

		done := make(chan bool)
		go func() { done <- m.lockTimeout(time.Second) }()

		synctest.Wait()
		m.state.Or(stateStarving)

		if <-done {
			t.Fatal("acquired a held mutex")
		}

		if s := m.state.Load(); s != stateUnit {
			t.Fatalf("state after the last waiter left = %#x, want %#x", s, stateUnit)
		}

		m.unlock()

		if !m.tryLock() {
			t.Fatal("canceled waiter left the mutex unusable")
		}

		m.unlock()
	})
}

func TestSemaphoreMisusedReleaseWindow(t *testing.T) {
	acquirers := []struct {
		name    string
		acquire func(*Semaphore) bool
	}{
		{"Acquire", func(s *Semaphore) bool { s.Acquire(); return true }},
		{"AcquireContext", func(s *Semaphore) bool { return s.AcquireContext(context.Background()) == nil }},
		{"TryAcquireTimeout", func(s *Semaphore) bool { return s.TryAcquireTimeout(time.Hour) }},
	}
	for _, a := range acquirers {
		for _, capacity := range []int{1, 2} {
			t.Run(a.name+"/"+strconv.Itoa(capacity), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					s := NewSemaphore(capacity)
					s.lock.state.Add(^(stateUnit - 1)) // the decrement of the misused Release

					var done, ok atomic.Bool
					go func() {
						ok.Store(a.acquire(s))
						done.Store(true)
					}()

					synctest.Wait()

					if done.Load() {
						t.Fatalf("returned %v while the release was being undone", ok.Load())
					}

					if r := undoMisusedRelease(s); r != "nsync: release without acquisition" {
						t.Errorf("undoing the release panicked with %v", r)
					}

					synctest.Wait()

					if !done.Load() || !ok.Load() {
						t.Fatalf("after the release was undone: returned %v, acquired %v", done.Load(), ok.Load())
					}

					if v := s.Value(); v != 1 {
						t.Fatalf("Value() = %d, want 1", v)
					}

					s.Release()

					if st := s.lock.state.Load(); st != 0 {
						t.Fatalf("state = %#x, want 0", st)
					}
				})
			})
		}
	}
}

// undoMisusedRelease runs the second half of a Release without an acquisition
// and returns its panic value.
func undoMisusedRelease(s *Semaphore) (r any) {
	defer func() { r = recover() }()

	s.releaseSlow()

	return nil
}

// Overlapping misused releases leave the word negative until the last one is
// undone; only then may waiters be woken, and the word must end up idle.
func TestSemaphoreOverlappingMisusedReleases(t *testing.T) {
	for _, contended := range []bool{false, true} {
		t.Run("queue="+strconv.FormatBool(contended), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s := NewSemaphore(1)
				if contended { // allocate the wait queue, then empty it
					s.Acquire()
					go func() { s.Acquire(); s.Release() }()

					synctest.Wait()
					s.Release()
					synctest.Wait()
				}

				s.lock.state.Add(^(stateUnit - 1))
				s.lock.state.Add(^(stateUnit - 1))

				for range 2 {
					if r := undoMisusedRelease(s); r != "nsync: release without acquisition" {
						t.Fatalf("undoing a release panicked with %v", r)
					}
				}

				if st := s.lock.state.Load(); st != 0 {
					t.Fatalf("state = %#x, want 0", st)
				}

				if !s.TryAcquire() {
					t.Fatal("TryAcquire failed on an idle semaphore")
				}

				s.Release()
			})
		})
	}
}

// An acquirer that gives up while a misused release is being undone must not
// clear bits of the wrapped count.
func TestSemaphoreWaiterGivesUpDuringMisusedRelease(t *testing.T) {
	acquirers := []struct {
		name    string
		acquire func(*Semaphore) bool
	}{
		{"TryAcquireTimeout", func(s *Semaphore) bool { return s.TryAcquireTimeout(time.Second) }},
		{"AcquireContext", func(s *Semaphore) bool {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()

			return s.AcquireContext(ctx) == nil
		}},
	}
	for _, a := range acquirers {
		t.Run(a.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s := NewSemaphore(1)
				s.lock.state.Add(^(stateUnit - 1))

				var done, ok atomic.Bool
				go func() {
					ok.Store(a.acquire(s))
					done.Store(true)
				}()

				time.Sleep(2 * time.Second)
				synctest.Wait()

				if !done.Load() || ok.Load() {
					t.Fatalf("returned %v, acquired %v; want a timeout", done.Load(), ok.Load())
				}

				if r := undoMisusedRelease(s); r != "nsync: release without acquisition" {
					t.Fatalf("undoing the release panicked with %v", r)
				}

				if st := s.lock.state.Load(); st != 0 {
					t.Fatalf("state = %#x, want 0", st)
				}
			})
		})
	}
}

// The negative flag must sit directly above the count, so that every
// decrement below zero sets it, and below the starving and retired flags.
func TestNegativeFlagIsAboveCount(t *testing.T) {
	if (maxPermits+1)<<stateUnitShift != stateNegative {
		t.Fatalf("maxPermits+1 = %#x permits, stateNegative = %#x", maxPermits+1, stateNegative)
	}

	if stateNegative<<1 != stateStarving || stateStarving<<1 != stateRetired {
		t.Fatalf("flags %#x, %#x, %#x are not adjacent", stateNegative, stateStarving, stateRetired)
	}
}

// misusedRelease calls Release without an acquisition and returns its panic.
func misusedRelease(s *Semaphore) (r any) {
	defer func() { r = recover() }()

	s.Release()

	return nil
}

// While a semaphore hands a permit to a starving waiter, its releasing
// holders have already decremented the count, to zero once every permit is
// released. A misused release that lands then borrows from the starving flag
// instead of wrapping around; it must still be detected, and the waiter must
// get the permit.
func TestSemaphoreMisusedReleaseDuringHandoff(t *testing.T) {
	for _, capacity := range []int{1, 2} {
		t.Run(strconv.Itoa(capacity), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s := NewSemaphore(capacity)
				for range capacity {
					s.Acquire()
				}

				var acquired atomic.Bool

				go func() {
					s.Acquire()
					acquired.Store(true)
				}()

				synctest.Wait()
				s.lock.state.Or(stateStarving) // the queue starves

				for range capacity { // every holder's Release decrements
					s.lock.state.Add(^(stateUnit - 1))
				}

				if r := misusedRelease(s); r != "nsync: release without acquisition" {
					t.Fatalf("misused release during a handoff panicked with %v", r)
				}

				for range capacity { // the holders' Releases finish
					s.releaseSlow()
				}

				synctest.Wait()

				if !acquired.Load() {
					t.Fatal("the waiter did not get a released permit")
				}

				s.Release()

				if st := s.lock.state.Load(); st != 0 {
					t.Fatalf("state = %#x, want 0", st)
				}
			})
		})
	}
}

// A valid Release on its slow path can see the word that a concurrent misused
// Release made negative. Only one of them may undo a decrement and panic, and
// the word must end up idle.
func TestSemaphoreMisusedReleaseRacesValidRelease(t *testing.T) {
	if runtime.GOMAXPROCS(0) < 2 {
		t.Skip("needs two Ps")
	}

	trials := 20000
	if testing.Short() || raceEnabled {
		trials = 2000
	}

	release := func(s *Semaphore) (panicked bool) {
		defer func() { panicked = recover() != nil }()

		s.Release()

		return false
	}
	for i := range trials {
		s := NewSemaphore(1)
		s.lock.waitQueue()
		s.lock.state.Store(stateUnit | stateQueued) // held, with waiters flagged

		var (
			ready  atomic.Int32
			panics atomic.Int32
			wg     sync.WaitGroup
		)
		for range 2 {
			wg.Go(func() {
				ready.Add(1)

				for ready.Load() < 2 {
				}

				if release(s) {
					panics.Add(1)
				}
			})
		}

		wg.Wait()

		if n, st := panics.Load(), s.lock.state.Load(); n != 1 || st&^stateQueued != 0 {
			t.Fatalf("trial %d: %d panics, state %#x; want 1 panic and an idle word", i, n, st)
		}
	}
}

// A waiter woken while the word is negative must not set the starving flag:
// in a negative word that bit belongs to the wrapped count, and undoing the
// release would otherwise carry into the retired flag.
func TestSemaphoreWaiterRetriesDuringMisusedRelease(t *testing.T) {
	handoffOverride.Store(1) // every waiter counts as starving
	defer handoffOverride.Store(0)

	synctest.Test(t, func(t *testing.T) {
		s := NewSemaphore(1)
		s.Acquire()

		var acquired atomic.Bool

		go func() {
			s.Acquire()
			acquired.Store(true)
		}()

		synctest.Wait()
		time.Sleep(time.Millisecond) // the waiter has now waited long enough to starve
		s.lock.state.Or(stateStarving)
		s.lock.state.Add(^(stateUnit - 1)) // the holder's Release decrements
		s.lock.state.Add(^(stateUnit - 1)) // a misused Release decrements
		s.lock.releaseAdded(s.limit)       // the holder's slow path wakes the waiter
		synctest.Wait()                    // which loses and parks again

		if acquired.Load() {
			t.Fatal("acquired while the word was negative")
		}

		if r := undoMisusedRelease(s); r != "nsync: release without acquisition" {
			t.Fatalf("undoing the release panicked with %v", r)
		}

		synctest.Wait()

		if !acquired.Load() {
			t.Fatal("the waiter did not get the permit after the undo")
		}

		s.Release()

		if st := s.lock.state.Load(); st != 0 {
			t.Fatalf("state = %#x, want 0", st)
		}
	})
}

// On 32-bit platforms a shard keeps only the low 32 bits of its cached lock's
// name hash. Names whose hashes share those bits must still get their own locks.
