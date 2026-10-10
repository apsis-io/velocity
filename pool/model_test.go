// Copyright (C) 2025-2026 Malformed C. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pool_test

import (
	"context"
	"errors"
	"math"
	"testing"

	"github.com/apsis-io/velocity/ownership"
	"github.com/apsis-io/velocity/pool"
)

// errNewFailed is the sentinel the fuzzed pool's New fails with when the
// driver has toggled construction failures on.
var errNewFailed = errors.New("fuzz: New failed")

// FuzzPoolModel drives one pool through a byte stream of lifecycle
// operations and holds the implementation against an exact model of the
// counters after every step. The driver is sequential, so every outcome is
// deterministic — a Get either finds capacity or the op is skipped (a
// blocking Get would make the model nondeterministic), New either succeeds
// or fails by toggle — which is what makes exact equality worth asserting:
// the counters are the package's public story about itself, and a counter
// that drifts by one is a lie the Stats consumer repeats.
//
// Operations (op % 12): get, release, double-release, discard, discard a
// spent handle, Value on live and spent, Move, Close (twice), setNewFail,
// and a full recheck. Gating skips ops that would block on capacity rather
// than waiting, so nothing here needs a goroutine or a timeout.
func FuzzPoolModel(f *testing.F) {
	f.Add([]byte{0, 0, 0, 1, 0, 3, 5, 0, 1, 6})
	f.Add([]byte{8, 0, 0, 0, 1, 1, 5, 0, 6, 2})
	f.Add([]byte{0, 7, 4, 1, 5, 0, 9, 5, 6, 3})
	f.Add([]byte{0, 0, 0, 0, 0, 0, 0, 0, 5, 5, 0, 6})
	f.Fuzz(func(t *testing.T, ops []byte) {
		ctx := context.Background()

		newFails := false
		p := pool.Must(pool.New(pool.Config[int]{
			New: func(context.Context) (int, error) {
				if newFails {
					return 0, errNewFailed
				}

				return 7, nil
			},
			Max: 8,
		}))

		// model mirrors the pool's documented accounting: idle plus checked
		// out is total; served and created advance only on a successful Get;
		// a put destroys (retired, plus failed if discarded) unless the pool
		// is open and the resource was not discarded, in which case it
		// rejoins idle.
		var model struct {
			idle, total     int
			created, served uint64
			retired, failed uint64
			closed          bool
		}

		var live, spent []*pool.Checkout[int]

		check := func(stage string) {
			t.Helper()

			got := p.Stats()
			want := pool.Stats{
				Idle:         model.idle,
				InUse:        model.total - model.idle,
				Max:          8,
				Created:      model.created,
				PassesServed: model.served,
				Retired:      model.retired,
				FailedPasses: model.failed,
			}

			if got != want {
				t.Fatalf("%s: stats %+v, want %+v", stage, got, want)
			}

			if model.served == 0 {
				if got.ReuseRate() != 0 {
					t.Fatalf("%s: ReuseRate %v with no passes served", stage, got.ReuseRate())
				}

				return
			}

			wantRate := float64(model.served-model.created) / float64(model.served)
			if math.Abs(got.ReuseRate()-wantRate) > 1e-9 {
				t.Fatalf("%s: ReuseRate %v, want %v", stage, got.ReuseRate(), wantRate)
			}
		}

		for _, op := range ops {
			switch op % 12 {
			case 0:
				// A Get is skipped when it would wait on capacity; with a
				// free permit it succeeds or fails on New, never blocks.
				// Expected-failure Gets stay anonymous — they hold nothing.
				if model.total >= 8 {
					continue
				}

				if model.closed {
					if _, err := p.Get(ctx); !errors.Is(err, pool.ErrClosed) {
						t.Fatalf("Get after Close: %v", err)
					}

					continue
				}

				if newFails && model.idle == 0 {
					if _, err := p.Get(ctx); !errors.Is(err, errNewFailed) {
						t.Fatalf("Get with failing New: %v", err)
					}

					continue
				}

				c, err := p.Get(ctx)
				if err != nil {
					t.Fatalf("Get: %v", err)
				}

				model.served++
				if model.idle > 0 {
					model.idle--
				} else {
					model.created++
					model.total++
				}

				live = append(live, c)

			case 1:
				if len(live) == 0 {
					continue
				}

				c := live[0]
				live = live[1:]

				if err := c.Release(); err != nil {
					t.Fatalf("Release: %v", err)
				}

				if !model.closed {
					model.idle++
				} else {
					model.total--
					model.retired++
				}

				spent = append(spent, c)

			case 2:
				// Release is exactly-once: a spent handle replays its result
				// and moves nothing.
				if len(spent) == 0 {
					continue
				}

				if err := spent[0].Release(); err != nil {
					t.Fatalf("double Release: %v", err)
				}

			case 3:
				if len(live) == 0 {
					continue
				}

				c := live[0]
				live = live[1:]

				if err := c.Discard(); err != nil {
					t.Fatalf("Discard: %v", err)
				}

				model.total--
				model.failed++
				model.retired++

				spent = append(spent, c)

			case 4:
				// Discard on a spent handle is Release's exactly-once result.
				if len(spent) == 0 {
					continue
				}

				if err := spent[0].Discard(); err != nil {
					t.Fatalf("Discard on released: %v", err)
				}

			case 5:
				v, err := func() (int, error) {
					if len(live) > 0 {
						return live[0].Value()
					}

					if len(spent) > 0 {
						return spent[0].Value()
					}

					return 0, nil
				}()

				if len(live) == 0 && len(spent) == 0 {
					continue
				}

				if len(live) > 0 {
					if err != nil {
						t.Fatalf("Value on live checkout: %v", err)
					}

					if v != 7 {
						t.Fatalf("Value on live checkout: got %d, want 7", v)
					}
				} else if !errors.Is(err, pool.ErrClosed) && !isReleasedErr(err) {
					t.Fatalf("Value on spent checkout: %v", err)
				}

			case 6:
				if len(live) == 0 {
					continue
				}

				next, err := live[0].Move()
				if err != nil {
					t.Fatalf("Move: %v", err)
				}

				spent = append(spent, live[0])
				live[0] = next

			case 7:
				if err := p.Close(); err != nil {
					t.Fatalf("Close: %v", err)
				}

				if !model.closed {
					model.closed = true
					model.retired += uint64(model.idle)
					model.total -= model.idle
					model.idle = 0
				}

			case 8:
				newFails = !newFails

			case 9:
				// A failing construction leaves nothing behind: no counters
				// move and the permit comes back, which this re-probe finds.
				// New is only consulted on a miss, so idle resources satisfy
				// the Get without it.
				if model.closed || model.total >= 8 || !newFails || model.idle > 0 {
					continue
				}

				before := p.Stats()

				if _, err := p.Get(ctx); !errors.Is(err, errNewFailed) {
					t.Fatalf("expected failing Get, got %v", err)
				}

				if after := p.Stats(); after != before {
					t.Fatalf("failing Get moved counters: %+v -> %+v", before, after)
				}

			case 10:
				// Every live checkout still holds its value; the pool's
				// story about them matches the model.
				for i, c := range live {
					if v, err := c.Value(); err != nil || v != 7 {
						t.Fatalf("live[%d].Value() = %d, %v", i, v, err)
					}
				}

				check("recheck")

			case 11:
				// Release is callable on a spent handle after Close too: the
				// resource was already destroyed at Close time.
				if len(spent) == 0 || !model.closed {
					continue
				}

				if err := spent[0].Release(); err != nil {
					t.Fatalf("Release after Close: %v", err)
				}
			}

			check("op")
		}

		// Drain: everything live returns, the pool closes, the final story
		// must be exact.
		for len(live) > 0 {
			c := live[0]
			live = live[1:]

			if err := c.Release(); err != nil {
				t.Fatalf("drain Release: %v", err)
			}

			if !model.closed {
				model.idle++
			} else {
				model.total--
				model.retired++
			}
		}

		if err := p.Close(); err != nil {
			t.Fatalf("drain Close: %v", err)
		}

		if !model.closed {
			model.closed = true
			model.retired += uint64(model.idle)
			model.total -= model.idle
			model.idle = 0
		}

		check("drain")
	})
}

func isReleasedErr(err error) bool {
	var released *ownership.ReleasedError

	return errors.As(err, &released)
}
