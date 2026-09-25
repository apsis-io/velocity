package async_test

import (
	"context"
	"sync"
	"testing"

	"github.com/apsis-io/velocity/async"
)

// BenchmarkRWMutexParallel measures the contended case the first benchmark got
// wrong. That version spawned a goroutine per iteration inside b.Loop, so every
// sample included the cost of creating a goroutine and joining it — hundreds
// of nanoseconds against a lock that takes eleven — and the conclusion that
// async loses under contention was a measurement of the harness rather than of
// the lock.
//
// Here the workers are started once and loop, so what is timed is the lock
// doing the work it exists to do: several goroutines contending for the same
// reader count, which is the case sync.RWMutex's atomic path handles and
// async's mutex-plus-counter path does not.
func BenchmarkRWMutexParallel(b *testing.B) {
	const goroutines = 8

	ctx := context.Background()

	b.Run("read", func(b *testing.B) {
		b.Run("async", func(b *testing.B) {
			mine := async.NewRWMutex()

			var wg sync.WaitGroup
			for range goroutines {
				wg.Go(func() {
					for range b.N / goroutines {
						read, _ := mine.RLock(ctx)
						read.Release()
					}
				})
			}
			wg.Wait()
		})

		b.Run("sync", func(b *testing.B) {
			theirs := &sync.RWMutex{}

			var wg sync.WaitGroup
			for range goroutines {
				wg.Go(func() {
					for range b.N / goroutines {
						theirs.RLock()
						theirs.RUnlock()
					}
				})
			}
			wg.Wait()
		})
	})

	b.Run("write", func(b *testing.B) {
		b.Run("async", func(b *testing.B) {
			mine := async.NewRWMutex()

			var wg sync.WaitGroup
			for range goroutines {
				wg.Go(func() {
					for range b.N / goroutines {
						held, _ := mine.Lock(ctx)
						held.Release()
					}
				})
			}
			wg.Wait()
		})

		b.Run("sync", func(b *testing.B) {
			theirs := &sync.RWMutex{}

			var wg sync.WaitGroup
			for range goroutines {
				wg.Go(func() {
					for range b.N / goroutines {
						theirs.Lock()
						theirs.Unlock()
					}
				})
			}
			wg.Wait()
		})
	})
}
