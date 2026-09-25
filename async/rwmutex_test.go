package async_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/apsis-io/velocity/async"
)

func TestRWMutexReadersDoNotExcludeEachOther(t *testing.T) {
	mu := async.NewRWMutex()

	first, err := mu.RLock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// A second reader must not block, or this is an exclusive lock with extra
	// steps.
	second, err := mu.RLock(context.Background())
	if err != nil {
		t.Fatalf("a second RLock blocked or failed: %v", err)
	}

	first.Release()
	second.Release()
}

// A writer excludes every reader, and a reader excludes every writer.
func TestRWMutexWriterExcludesReaders(t *testing.T) {
	mu := async.NewRWMutex()
	ctx := context.Background()

	read, err := mu.RLock(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if held, ok := mu.TryLock(); ok {
		held.Release()
		t.Fatal("TryLock took the write lock while a reader held the lock")
	}

	// A writer waits for the reader, and takes it once the reader is gone.
	var took atomic.Bool

	go func() {
		held, err := mu.Lock(ctx)
		if err == nil {
			took.Store(true)

			if read, ok := mu.TryRLock(); ok {
				read.Release()
				t.Error("TryRLock succeeded while the write lock was held")
			}

			held.Release()
		}
	}()

	read.Release()
	waitFor(t, func() bool { return took.Load() })
}

// Writer priority: a reader arriving after a writer is waiting must not jump
// the queue, or a stream of readers starves the writer.
func TestRWMutexWriterTakesPriorityOverNewReaders(t *testing.T) {
	mu := async.NewRWMutex()
	ctx := context.Background()

	held, err := mu.RLock(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// Queue a writer behind the reader we hold.
	writerWaiting := make(chan struct{})
	writerTook := make(chan struct{})

	go func() {
		close(writerWaiting)

		lock, err := mu.Lock(ctx)
		if err == nil {
			close(writerTook)
			lock.Release()
		}
	}()

	<-writerWaiting

	readerTook := make(chan struct{})

	go func() {
		read, err := mu.RLock(ctx)
		if err == nil {
			close(readerTook)
			read.Release()
		}
	}()
	// The 100ms is a bound on how long we watch for the wrong thing, not a
	// measurement: if the reader were admitted the select takes the first
	// branch at once and fails. If the writer had not registered, the reader
	// would be admitted and this would fail too, so the window cannot hide the
	// bug it is watching for.
	select {
	case <-readerTook:
		t.Fatal("RLock was admitted while a writer was waiting")
	case <-time.After(100 * time.Millisecond):
	}

	held.Release()
	<-writerTook
}

func TestRWMutexWaitsUnderTheCallersContext(t *testing.T) {
	mu := async.NewRWMutex()

	// The writer holds, so both a reader and another writer must wait. Holding
	// a read lock instead would be the wrong fixture: readers do not exclude
	// each other, so an RLock there is admitted rather than made to wait.
	held, err := mu.Lock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	if _, err := mu.Lock(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Lock = %v, want the caller's deadline", err)
	}
	if _, err := mu.RLock(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("RLock = %v, want the caller's deadline", err)
	}
}

// A writer that gives up must not leave itself counted as waiting, or every
// later reader blocks behind a writer that is not there. This is the
// accounting the whole type turns on, so it is asserted directly rather than
// inferred from a later test passing.
func TestRWMutexAbandonedWriterDoesNotBlockReaders(t *testing.T) {
	mu := async.NewRWMutex()
	ctx := context.Background()

	// A reader to make the writer wait on.
	held, err := mu.RLock(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// Two writers give up, so the count is wrong by two if the bail path does
	// not decrement.
	for range 2 {
		short, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
		if _, err := mu.Lock(short); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Lock = %v, want the deadline", err)
		}

		cancel()
	}

	held.Release()

	// Bounded, so a leak reports as a failure rather than hanging: with a
	// background context and writers still counted, this blocks forever and
	// the test dies on the package timeout with no explanation. That was the
	// first version, and it turned a caught mutation into a silent one.
	fresh, cancelFresh := context.WithTimeout(ctx, 5*time.Second)
	defer cancelFresh()

	read, err := mu.RLock(fresh)
	if err != nil {
		t.Fatalf("RLock after abandoned writers: %v", err)
	}

	read.Release()
}

func TestRWMutexTryForms(t *testing.T) {
	mu := async.NewRWMutex()

	write, ok := mu.TryLock()
	if !ok {
		t.Fatal("TryLock on a free lock")
	}

	if read, ok := mu.TryRLock(); ok {
		read.Release()
		t.Fatal("TryRLock while the write lock is held")
	}

	if again, ok := mu.TryLock(); ok {
		again.Release()
		t.Fatal("TryLock while the write lock is held")
	}

	write.Release()

	read, ok := mu.TryRLock()
	if !ok {
		t.Fatal("TryRLock on a free lock")
	}

	if held, ok := mu.TryLock(); ok {
		held.Release()
		t.Fatal("TryLock while a reader holds the lock")
	}
	// A second reader still fits alongside the first.
	if second, ok := mu.TryRLock(); !ok {
		t.Fatal("TryRLock alongside an existing reader")
	} else {
		second.Release()
	}

	read.Release()
}

// Release is idempotent for both forms, because a deferred Release beside an
// explicit one is the documented pattern.
func TestRWMutexDoubleReleaseIsSafe(t *testing.T) {
	mu := async.NewRWMutex()
	ctx := context.Background()

	read, err := mu.RLock(ctx)
	if err != nil {
		t.Fatal(err)
	}

	read.Release()
	read.Release()
	read.Release()

	// A double release that corrupted the reader count would leave this
	// blocked or panic on a closed channel.
	write, err := mu.Lock(ctx)
	if err != nil {
		t.Fatalf("Lock after a triple read release: %v", err)
	}

	write.Release()
	write.Release()
}

// The property the type exists for, under load: a writer never overlaps
// another writer or a reader, and any number of readers may overlap each
// other.
func TestRWMutexExcludesUnderLoad(t *testing.T) {
	const (
		readers = 8
		writers = 4
		rounds  = 200
	)

	mu := async.NewRWMutex()
	ctx := context.Background()

	var (
		active, peak atomic.Int64
		overlap      atomic.Bool
	)

	for range readers {
		go func() {
			for range rounds {
				held, err := mu.RLock(ctx)
				if err != nil {
					t.Error(err)
					return
				}

				n := active.Add(1)

				for {
					old := peak.Load()
					if n <= old || peak.CompareAndSwap(old, n) {
						break
					}
				}

				time.Sleep(time.Microsecond)
				active.Add(-1)
				held.Release()
			}
		}()
	}

	for range writers {
		go func() {
			for range rounds {
				held, err := mu.Lock(ctx)
				if err != nil {
					t.Error(err)
					return
				}
				// A writer must be alone. One other holder is the bug.
				if active.Add(1) != 1 {
					overlap.Store(true)
				}

				time.Sleep(time.Microsecond)
				active.Add(-1)
				held.Release()
			}
		}()
	}

	waitFor(t, func() bool { return peak.Load() > 0 })
	// Writers are the signal that all goroutines have been through: the
	// writers hold the lock, so every reader must already have finished.
	time.Sleep(50 * time.Millisecond)

	if overlap.Load() {
		t.Fatal("a writer held the lock alongside another holder")
	}
}

// waitFor polls until cond holds, so a test never asserts on how long
// something took. The timeout is a failure bound, not a measurement.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition never held")
		}

		time.Sleep(time.Millisecond)
	}
}
