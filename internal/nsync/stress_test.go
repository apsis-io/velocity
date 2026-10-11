package nsync

import (
	"context"
	"math/rand/v2"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Each stress test runs with the default handoff threshold, and with handoff
// after any lost race, so most contended acquisitions are handed over.
func forEachHandoffMode(t *testing.T, f func(t *testing.T)) {
	for _, mode := range []struct {
		name      string
		threshold time.Duration
	}{{"Barging", 0}, {"Handoff", time.Nanosecond}} {
		t.Run(mode.name, func(t *testing.T) {
			handoffOverride.Store(int32(mode.threshold))
			defer handoffOverride.Store(0)

			f(t)
		})
	}
}

func stressRounds() int {
	if testing.Short() || raceEnabled {
		return 60
	}

	return 250
}

// acquireRandomly acquires through a randomly chosen method, including
// timeouts that expire while queued and contexts canceled concurrently.
func acquireRandomly(rng *rand.Rand, lock func(), try func() bool, timed func(time.Duration) bool, withContext func(context.Context) error) bool {
	switch rng.IntN(7) {
	case 0, 1:
		lock()
		return true
	case 2:
		return try()
	case 3:
		return timed(time.Duration(rng.IntN(3)) * time.Microsecond)
	case 4:
		return timed(time.Duration(rng.IntN(300)) * time.Microsecond)
	case 5:
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(rng.IntN(200))*time.Microsecond)
		defer cancel()

		return withContext(ctx) == nil
	default:
		ctx, cancel := context.WithCancel(context.Background())

		go func() {
			runtime.Gosched()
			cancel()
		}()

		return withContext(ctx) == nil
	}
}

func TestStressTryMutex(t *testing.T) {
	forEachHandoffMode(t, func(t *testing.T) {
		m := NewTryMutex()

		var (
			holders atomic.Int32
			counter int
			wg      sync.WaitGroup
		)
		for g := range 48 {
			wg.Go(func() {
				rng := rand.New(rand.NewPCG(uint64(g), 1))
				for range stressRounds() {
					if !acquireRandomly(rng, m.Lock, m.TryLock, m.TryLockTimeout, m.LockContext) {
						continue
					}
					if holders.Add(1) != 1 {
						t.Error("two goroutines hold the mutex")
					}

					counter++

					if rng.IntN(4) == 0 {
						runtime.Gosched()
					}

					holders.Add(-1)
					m.Unlock()
				}
			})
		}

		wg.Wait()

		if s := m.state.state.Load(); s != 0 {
			t.Fatalf("state = %#x after all goroutines left, want 0", s)
		}
	})
}

func TestStressSemaphore(t *testing.T) {
	forEachHandoffMode(t, func(t *testing.T) {
		for _, capacity := range []int{1, 2, 3, 8} {
			s := NewSemaphore(capacity)

			var (
				holders atomic.Int32
				wg      sync.WaitGroup
			)
			for g := range 48 {
				wg.Go(func() {
					rng := rand.New(rand.NewPCG(uint64(g), 2))
					for range stressRounds() {
						if !acquireRandomly(rng, s.Acquire, s.TryAcquire, s.TryAcquireTimeout, s.AcquireContext) {
							continue
						}

						if n := holders.Add(1); n > int32(capacity) {
							t.Errorf("%d holders, capacity %d", n, capacity)
						}

						if rng.IntN(4) == 0 {
							runtime.Gosched()
						}

						holders.Add(-1)
						s.Release()
					}
				})
			}

			wg.Wait()

			if st, gate := s.lock.state.Load(), s.gate.state.Load(); st != 0 || gate != 0 {
				t.Fatalf("capacity %d: state = %#x, gate = %#x after all goroutines left, want 0", capacity, st, gate)
			}
		}
	})
}
