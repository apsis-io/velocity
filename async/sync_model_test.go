// Copyright (C) 2025-2026 Malformed C. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package async_test

import (
	"context"
	"testing"

	"github.com/apsis-io/velocity/async"
)

// FuzzSyncModel drives the sync primitives — Semaphore, Mutex, RWMutex —
// through a byte stream of acquire/release/Try operations and holds each
// against an exact model of what it has outstanding. The primitives expose
// no Stats to read, so the model validates itself behaviorally: every Try
// must succeed exactly when the model says a slot is free, every release is
// exactly-once, and a spent permit's later release moves nothing. The
// driver is sequential and gates every op that would block, so every
// outcome is deterministic and the Try assertions are exact rather than
// racy.
//
// These are the internals the nsync swaps moved; the hand-written tests
// stayed green through that change, and this is the instrument that would
// not have.
//
// Operations (op % 16): semaphore acquire/Try/release/double-release,
// mutex Lock/Try/release/double-release, RWMutex RLock/TryRLock/Lock/
// TryLock/release/double-release, a read-specific release, and a final
// cross-primitive probe.
func FuzzSyncModel(f *testing.F) {
	f.Add([]byte{0, 0, 2, 0, 1, 3, 6})
	f.Add([]byte{4, 5, 6, 4, 8, 9, 14, 6})
	f.Add([]byte{8, 8, 10, 15, 12, 8, 13, 14})
	f.Add([]byte{0, 4, 8, 10, 1, 5, 9, 11, 15, 12, 6, 14, 2})
	f.Fuzz(func(t *testing.T, ops []byte) {
		ctx := context.Background()

		sem, err := async.NewSemaphore(8)
		if err != nil {
			t.Fatal(err)
		}

		mu := async.NewMutex()
		rw := async.NewRWMutex()

		var (
			semHeld    int
			muHeld     bool
			rwReaders  int
			rwWriter   bool
			semLive    []*async.Permit
			semSpent   []*async.Permit
			muLive     []*async.Permit
			muSpent    []*async.Permit
			rwReadLive []*async.Permit
			rwWriteLiv []*async.Permit
			rwSpent    []*async.Permit
		)

		for _, op := range ops {
			switch op % 16 {
			case 0:
				// Gated on capacity, so the acquire cannot block and must
				// succeed.
				if semHeld >= 8 {
					continue
				}

				p, err := sem.Acquire(ctx)
				if err != nil {
					t.Fatalf("Acquire: %v", err)
				}

				semHeld++
				semLive = append(semLive, p)

			case 1:
				p, ok := sem.TryAcquire()
				if ok != (semHeld < 8) {
					t.Fatalf("TryAcquire = %v with %d of 8 held", ok, semHeld)
				}

				if ok {
					semHeld++
					semLive = append(semLive, p)
				}

			case 2:
				if len(semLive) == 0 {
					continue
				}

				p := semLive[0]
				semLive = semLive[1:]

				p.Release()
				semHeld--
				semSpent = append(semSpent, p)

			case 3:
				// Release is exactly-once: a spent permit's release moves
				// nothing and panics at nothing.
				if len(semSpent) == 0 {
					continue
				}

				semSpent[0].Release()

			case 4:
				if muHeld {
					continue
				}

				p, err := mu.Lock(ctx)
				if err != nil {
					t.Fatalf("Lock: %v", err)
				}

				muHeld = true
				muLive = append(muLive, p)

			case 5:
				p, ok := mu.TryLock()
				if ok != !muHeld {
					t.Fatalf("TryLock = %v with mutex held=%v", ok, muHeld)
				}

				if ok {
					muHeld = true
					muLive = append(muLive, p)
				}

			case 6:
				if len(muLive) == 0 {
					continue
				}

				p := muLive[0]
				muLive = muLive[1:]

				p.Release()
				muHeld = false
				muSpent = append(muSpent, p)

			case 7:
				if len(muSpent) == 0 {
					continue
				}

				muSpent[0].Release()

			case 8:
				// A blocking RLock is gated on the writer; readers coexist.
				if rwWriter {
					continue
				}

				p, err := rw.RLock(ctx)
				if err != nil {
					t.Fatalf("RLock: %v", err)
				}

				rwReaders++
				rwReadLive = append(rwReadLive, p)

			case 9:
				p, ok := rw.TryRLock()
				if ok != !rwWriter {
					t.Fatalf("TryRLock = %v with writer=%v", ok, rwWriter)
				}

				if ok {
					rwReaders++
					rwReadLive = append(rwReadLive, p)
				}

			case 10:
				if rwWriter || rwReaders > 0 {
					continue
				}

				p, err := rw.Lock(ctx)
				if err != nil {
					t.Fatalf("Lock: %v", err)
				}

				rwWriter = true
				rwWriteLiv = append(rwWriteLiv, p)

			case 11:
				p, ok := rw.TryLock()
				if ok != (rwReaders == 0 && !rwWriter) {
					t.Fatalf("TryLock = %v with readers=%d writer=%v", ok, rwReaders, rwWriter)
				}

				if ok {
					rwWriter = true
					rwWriteLiv = append(rwWriteLiv, p)
				}

			case 12:
				if len(rwWriteLiv) > 0 {
					p := rwWriteLiv[0]
					rwWriteLiv = rwWriteLiv[1:]

					p.Release()
					rwWriter = false
					rwSpent = append(rwSpent, p)
				} else if len(rwReadLive) > 0 {
					p := rwReadLive[0]
					rwReadLive = rwReadLive[1:]

					p.Release()
					rwReaders--
					rwSpent = append(rwSpent, p)
				}

			case 13:
				if len(rwSpent) == 0 {
					continue
				}

				rwSpent[0].Release()

			case 14:
				if len(rwReadLive) == 0 {
					continue
				}

				p := rwReadLive[0]
				rwReadLive = rwReadLive[1:]

				p.Release()
				rwReaders--
				rwSpent = append(rwSpent, p)

			case 15:
				// The probe: the model's view must be what the primitives
				// actually offer, right now, for every one of the three.
				if p, ok := sem.TryAcquire(); ok != (semHeld < 8) {
					t.Fatalf("probe: TryAcquire = %v with %d of 8 held", ok, semHeld)
				} else if ok {
					semHeld++
					p.Release()
					semHeld--
				}

				if p, ok := mu.TryLock(); ok != !muHeld {
					t.Fatalf("probe: TryLock = %v with mutex held=%v", ok, muHeld)
				} else if ok {
					muHeld = true
					p.Release()
					muHeld = false
				}

				p, ok := rw.TryRLock()
				if ok != !rwWriter {
					t.Fatalf("probe: TryRLock = %v with writer=%v", ok, rwWriter)
				}

				if ok {
					rwReaders++
					p.Release()
					rwReaders--
				}
			}
		}

		// Drain everything live, then every primitive must offer a slot to
		// a fresh probe: nothing leaked, nothing stuck held.
		for len(semLive) > 0 {
			p := semLive[0]
			semLive = semLive[1:]

			p.Release()
			semHeld--
		}

		for len(muLive) > 0 {
			p := muLive[0]
			muLive = muLive[1:]

			p.Release()
			muHeld = false
		}

		for len(rwWriteLiv) > 0 {
			p := rwWriteLiv[0]
			rwWriteLiv = rwWriteLiv[1:]

			p.Release()
			rwWriter = false
		}

		for len(rwReadLive) > 0 {
			p := rwReadLive[0]
			rwReadLive = rwReadLive[1:]

			p.Release()
			rwReaders--
		}

		if p, ok := sem.TryAcquire(); !ok {
			t.Fatal("drain: semaphore offers no slot")
		} else {
			p.Release()
		}

		if p, ok := mu.TryLock(); !ok {
			t.Fatal("drain: mutex offers no slot")
		} else {
			p.Release()
		}

		if p, ok := rw.TryLock(); !ok {
			t.Fatal("drain: RWMutex offers no write slot")
		} else {
			p.Release()
		}
	})
}
