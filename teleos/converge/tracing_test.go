// Copyright (C) 2025-2026 Malformed C. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package converge_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/apsis-io/velocity/teleos"
	"github.com/apsis-io/velocity/teleos/converge"
)

// The daemon's pass units — observe and apply — are the caller's callbacks,
// so a span opened around the pass travels into them with the context. This
// pins that: a value the daemon puts in its context is visible in both, which
// is what makes pass spans caller-side and free.

type traceKey struct{}

func TestTracingContextReachesObserveAndApply(t *testing.T) {
	h := &podHarness{world: pod{wantNet: true, wantUnit: "running"}}

	engine, err := teleos.New(teleos.Config[pod, string]{Plan: anonStages(podInvariants())})
	if err != nil {
		t.Fatal(err)
	}

	var (
		observed atomic.Bool
		applied  atomic.Bool
		converg  = make(chan struct{}, 1)
	)

	c, err := converge.New(
		engine,
		func(ctx context.Context) (pod, error) {
			if ctx.Value(traceKey{}) == "pass" {
				observed.Store(true)
			}

			return h.observe(ctx)
		},
		func(ctx context.Context, eff string) error {
			if ctx.Value(traceKey{}) == "pass" {
				applied.Store(true)
			}

			return h.apply(ctx, eff)
		},
		converge.Config[pod, string]{
			OnReport: func(_ context.Context, r teleos.Report[pod, string]) {
				if r.Status == teleos.Converged {
					select {
					case converg <- struct{}{}:
					default:
					}
				}
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	// The pass context: the span the daemon opened around its loop.
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), traceKey{}, "pass"))
	defer cancel()

	go c.Run(ctx)

	c.Wake()

	select {
	case <-converg:
	case <-time.After(5 * time.Second):
		cancel()

		t.Fatal("never converged")
	}

	cancel()

	if !observed.Load() || !applied.Load() {
		t.Fatalf("observe=%v apply=%v — the pass context did not reach the caller's callbacks", observed.Load(), applied.Load())
	}
}
