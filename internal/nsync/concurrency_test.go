package nsync

import (
	"math"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestTryMutexValueCopy(t *testing.T) {
	m := NewTryMutex()
	copy := *m

	var _ sync.Locker = copy

	m.Lock()
	if copy.TryLock() {
		t.Fatal("copy did not share the lock")
	}
	copy.Unlock()

	if !m.TryLock() {
		t.Fatal("unlock through copy was not visible")
	}
	m.Unlock()
}

func TestTryMutexMutualExclusionAndPublication(t *testing.T) {
	m := NewTryMutex()

	var (
		count, checksum int
		wg              sync.WaitGroup
	)
	for range 32 {
		wg.Go(func() {
			for range 200 {
				m.Lock()
				if checksum != count*17 {
					t.Error("protected writes were not published")
				}

				count++

				runtime.Gosched()

				checksum = count * 17
				m.Unlock()
			}
		})
	}

	wg.Wait()

	if count != 6400 {
		t.Fatalf("count = %d, want 6400", count)
	}
}

func TestSemaphoreConcurrentCapacity(t *testing.T) {
	for _, capacity := range []int{1, 2, 8, 64} {
		s := NewSemaphore(capacity)

		var (
			active atomic.Int32
			wg     sync.WaitGroup
		)
		for range 64 {
			wg.Go(func() {
				for range 50 {
					s.Acquire()

					if n := active.Add(1); n > int32(capacity) {
						t.Errorf("active = %d, capacity = %d", n, capacity)
					}

					runtime.Gosched()
					active.Add(-1)
					s.Release()
				}
			})
		}

		wg.Wait()

		if s.Value() != 0 || active.Load() != 0 {
			t.Fatal("semaphore leaked an acquisition")
		}
	}
}

func TestSemaphoreLargeCapacity(t *testing.T) {
	s := NewSemaphore(math.MaxInt)
	s.Acquire()

	if !s.TryAcquire() || s.Value() != 2 {
		t.Fatal("large capacity does not work")
	}

	s.Release()
	s.Release()
}

func TestGeneralSemaphoreNonblockingAndInvalidRelease(t *testing.T) {
	s := NewSemaphore(8)
	mustPanic(t, s.Release)

	for range 8 {
		if !s.TryAcquireTimeout(0) {
			t.Fatal("nonblocking acquisition rejected an available slot")
		}
	}

	for _, timeout := range []time.Duration{0, -time.Second} {
		if s.TryAcquireTimeout(timeout) {
			t.Fatal("nonblocking acquisition exceeded capacity")
		}
	}

	for range 8 {
		s.Release()
	}

	mustPanic(t, s.Release)

	if !s.TryAcquire() {
		t.Fatal("invalid release corrupted the counter")
	}

	s.Release()
}

func TestTimedWaitersDoNotStrandBlockingWaiter(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := NewTryMutex()
		m.Lock()

		var timed sync.WaitGroup
		for range 16 {
			timed.Go(func() {
				if m.TryLockTimeout(time.Second) {
					m.Unlock()
					t.Error("acquired before release")
				}
			})
		}

		finished := make(chan struct{})

		go func() {
			m.Lock()

			if m.TryLock() {
				t.Error("timed acquisition did not exclude TryLock")
			}

			m.Unlock()
			close(finished)
		}()

		synctest.Wait()
		time.Sleep(time.Second)
		timed.Wait()
		m.Unlock()
		<-finished

		if !m.TryLock() {
			t.Fatal("a canceled waiter retained the lock")
		}
		m.Unlock()
	})
}

func TestTimedWaitersDifferentDeadlines(t *testing.T) {
	for _, capacity := range []int{1, 2, 8} {
		synctest.Test(t, func(t *testing.T) {
			s := NewSemaphore(capacity)
			for range capacity {
				s.Acquire()
			}

			var (
				acquired, expired atomic.Int32
				wg                sync.WaitGroup
			)
			for i := 1; i <= 8; i++ {
				wg.Go(func() {
					if s.TryAcquireTimeout(time.Duration(i) * time.Millisecond) {
						acquired.Add(1)
						s.Release()
					} else {
						expired.Add(1)
					}
				})
			}

			synctest.Wait()
			time.Sleep(4500 * time.Microsecond)

			for range capacity {
				s.Release()
			}

			wg.Wait()

			if acquired.Load() != 4 || expired.Load() != 4 {
				t.Errorf("acquired = %d, expired = %d, want 4 each", acquired.Load(), expired.Load())
			}

			if s.Value() != 0 {
				t.Fatal("timed wait leaked a slot")
			}
		})
	}
}

func TestTimeoutConcurrentWithRelease(t *testing.T) {
	for _, capacity := range []int{1, 2, 8} {
		for range 200 {
			s := NewSemaphore(capacity)
			for range capacity {
				s.Acquire()
			}

			var wg sync.WaitGroup
			wg.Go(func() {
				if s.TryAcquireTimeout(time.Microsecond) {
					s.Release()
				}
			})
			wg.Go(func() {
				for range capacity {
					s.Release()
				}
			})
			wg.Wait()

			if s.Value() != 0 {
				t.Fatal("release/timeout race leaked a slot")
			}

			if !s.TryAcquire() {
				t.Fatal("semaphore unusable after release/timeout race")
			}

			s.Release()
		}
	}
}

func TestTinyTimeoutsUseTheClock(t *testing.T) {
	for _, timeout := range []time.Duration{time.Nanosecond, 100 * time.Nanosecond, time.Microsecond} {
		synctest.Test(t, func(t *testing.T) {
			m := NewTryMutex()
			m.Lock()
			start := time.Now()

			if m.TryLockTimeout(timeout) {
				t.Fatal("acquired a held mutex")
			}

			if elapsed := time.Since(start); elapsed != timeout {
				t.Errorf("waited %v, want %v", elapsed, timeout)
			}
			m.Unlock()

			s := NewSemaphore(2)
			s.Acquire()
			s.Acquire()

			start = time.Now()

			if s.TryAcquireTimeout(timeout) {
				t.Fatal("acquired a full semaphore")
			}

			if elapsed := time.Since(start); elapsed != timeout {
				t.Errorf("waited %v, want %v", elapsed, timeout)
			}

			s.Release()
			s.Release()
		})
	}
}
