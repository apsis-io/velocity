// Copyright (C) 2025-2026 Malformed C. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package converge_test

import (
	"context"
	"fmt"
	"sync"

	"github.com/apsis-io/velocity/teleos"
	"github.com/apsis-io/velocity/teleos/converge"
)

// demoHost is the world the example converges: one service wanted running.
type demoHost struct {
	mu             sync.Mutex
	wanted         bool
	running        bool
	applied        []string
	firstApply     chan struct{}
	firstApplyDone bool
}

func (h *demoHost) observe(context.Context) (demoState, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	return demoState{wanted: h.wanted, running: h.running}, nil
}

func (h *demoHost) apply(_ context.Context, eff string) error {
	h.mu.Lock()
	h.running = true
	h.applied = append(h.applied, eff)
	h.mu.Unlock()

	if !h.firstApplyDone {
		h.firstApplyDone = true
		close(h.firstApply)
	}

	return nil
}

type demoState struct {
	wanted, running bool
}

func Example() {
	h := &demoHost{wanted: true, firstApply: make(chan struct{})}

	engine, err := teleos.New(teleos.Config[demoState, string]{
		Plan: []teleos.Invariant[demoState, string]{
			teleos.Align(
				func(s demoState) bool { return s.wanted },
				func(s demoState) bool { return s.running },
				func(target bool, _ demoState) string {
					if target {
						return "START_SERVICE"
					}

					return "STOP_SERVICE"
				},
			),
		},
	})
	if err != nil {
		panic(err)
	}

	// The daemon: one goroutine waiting for wakes, observing, applying
	// effects, until the context ends. In a real service the wake comes
	// from watches; here the example sends one.
	converged := make(chan struct{}, 1)

	c, err := converge.New(engine, h.observe, h.apply, converge.Config[demoState, string]{
		OnReport: func(_ context.Context, r teleos.Report[demoState, string]) {
			if r.Status == teleos.Converged {
				select {
				case converged <- struct{}{}:
				default:
				}
			}
		},
	})
	if err != nil {
		panic(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go c.Run(ctx)

	c.Wake()

	<-h.firstApply

	select {
	case <-converged:
	case <-ctx.Done():
		panic("never converged")
	}

	cancel()

	h.mu.Lock()
	defer h.mu.Unlock()

	fmt.Println(h.applied)
	fmt.Println("one wake: observed, decided, applied — and the loop waits for the next")

	// Output:
	// [START_SERVICE]
	// one wake: observed, decided, applied — and the loop waits for the next
}
