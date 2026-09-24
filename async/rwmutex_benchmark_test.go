// Every critical section in this file is empty on purpose: the lock is the
// measurement. staticcheck flags that for the sync arms, where it recognises a
// real lock, and cannot flag it for the async arms, whose RLock is an ordinary
// method call — which is also why an inline directive there is reported as
// unmatched. One file-level directive is the honest form for this file.
//
//lint:file-ignore SA2001 the critical section is the measurement
package async_test

import (
	"context"
	"sync"
	"testing"

	"github.com/apsis-io/velocity/async"
)

// The comparison that matters for adopting RWMutex: against sync.RWMutex, on
// the three shapes a caller actually has. sync's version cannot be cancelled,
// which is the reason to prefer this one at all, and the numbers here are what
// that costs.
func BenchmarkRWMutex(b *testing.B) {
	ctx := context.Background()

	b.Run("uncontended read", func(b *testing.B) {
		mine := async.NewRWMutex()
		theirs := &sync.RWMutex{}

		b.Run("async", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				read, _ := mine.RLock(ctx)
				read.Release()
			}
		})
		b.Run("sync", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				theirs.RLock()
				theirs.RUnlock()
			}
		})
	})

	b.Run("uncontended write", func(b *testing.B) {
		mine := async.NewRWMutex()
		theirs := &sync.RWMutex{}

		b.Run("async", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				held, _ := mine.Lock(ctx)
				held.Release()
			}
		})
		b.Run("sync", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				theirs.Lock()
				theirs.Unlock()
			}
		})
	})

	b.Run("contended read x8", func(b *testing.B) {
		mine := async.NewRWMutex()
		theirs := &sync.RWMutex{}
		run(b, 8, func(i int) {
			read, _ := mine.RLock(ctx)
			read.Release()
		}, func() {
			theirs.RLock()
			theirs.RUnlock()
		})
	})

	b.Run("contended write x8", func(b *testing.B) {
		mine := async.NewRWMutex()
		theirs := &sync.RWMutex{}
		run(b, 8, func(int) {
			held, _ := mine.Lock(ctx)
			held.Release()
		}, func() {
			theirs.Lock()
			theirs.Unlock()
		})
	})
}

// run benchmarks two arms, one goroutine each, n of them.
func run(b *testing.B, n int, mine func(int), theirs func()) {
	b.Helper()
	b.Run("async", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			var wg sync.WaitGroup
			for i := range n {
				wg.Go(func() { mine(i) })
			}
			wg.Wait()
		}
	})
	b.Run("sync", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			var wg sync.WaitGroup
			for range n {
				wg.Go(theirs)
			}
			wg.Wait()
		}
	})
}

// The Permit grew a word for RWMutex's release hook, and Permit is allocated
// on every Semaphore and Mutex acquire. This measures that cost directly, so
// the number is known rather than assumed.
func BenchmarkSemaphoreAcquire(b *testing.B) {
	sem, err := async.NewSemaphore(1)
	if err != nil {
		b.Fatal(err)
	}
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		permit, err := sem.Acquire(ctx)
		if err != nil {
			b.Fatal(err)
		}
		permit.Release()
	}
}

func BenchmarkMutexLock(b *testing.B) {
	mu := async.NewMutex()
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		held, err := mu.Lock(ctx)
		if err != nil {
			b.Fatal(err)
		}
		held.Release()
	}
}
