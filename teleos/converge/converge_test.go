// Copyright (C) 2025-2026 Malformed C. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package converge_test

import (
	"context"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/apsis-io/velocity/async"
	"github.com/apsis-io/velocity/dedupe"
	"github.com/apsis-io/velocity/opcodes"
	"github.com/apsis-io/velocity/opruntime"
	"github.com/apsis-io/velocity/teleos"
	"github.com/apsis-io/velocity/teleos/converge"
)

type pod struct {
	wantNet, hasNet     bool
	wantUnit, unitState string
}

// podInvariants is the pod plan as string effects, for the loop tests.
func podInvariants() []teleos.Invariant[pod, string] {
	return []teleos.Invariant[pod, string]{
		teleos.Align(
			func(w pod) bool { return w.wantNet },
			func(w pod) bool { return w.hasNet },
			func(target bool, _ pod) string {
				if target {
					return "NETNS_CREATE"
				}

				return "NETNS_DELETE"
			},
		),
		teleos.Align(
			func(w pod) string { return w.wantUnit },
			func(w pod) string { return w.unitState },
			func(target string, _ pod) string {
				if target == "running" {
					return "UNIT_START"
				}

				return "UNIT_STOP"
			},
		),
	}
}

// podHarness is a world, its lock, and the sequence of applied effects.
type podHarness struct {
	mu      sync.Mutex
	world   pod
	applied []string
}

func (h *podHarness) observe(context.Context) (pod, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	return h.world, nil
}

func (h *podHarness) apply(_ context.Context, eff string) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	switch eff {
	case "NETNS_CREATE":
		h.world.hasNet = true
	case "UNIT_START":
		h.world.unitState = "running"
	case "NETNS_DELETE":
		h.world.hasNet = false
	case "UNIT_STOP":
		h.world.unitState = "stopped"
	}

	h.applied = append(h.applied, eff)

	return nil
}

func (h *podHarness) snapshot() (pod, []string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	return h.world, h.applied
}

func TestConvergerRunsToEquilibriumOnOneWake(t *testing.T) {
	h := &podHarness{world: pod{wantNet: true, wantUnit: "running"}}

	engine, err := teleos.New(teleos.Config[pod, string]{Plan: podInvariants()})
	if err != nil {
		t.Fatal(err)
	}

	converged := make(chan struct{}, 1)

	c, err := converge.New(engine, h.observe, h.apply, converge.Config[pod, string]{
		OnReport: func(_ context.Context, r teleos.Report[pod, string]) {
			if r.Status == teleos.Converged {
				select {
				case converged <- struct{}{}:
				default:
				}
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runErr := make(chan error, 1)

	go func() { runErr <- c.Run(ctx) }()

	// One wake converges the whole plan: the pass loop keeps stepping until
	// the engine rests.
	c.Wake()

	select {
	case <-converged:
	case <-time.After(5 * time.Second):
		t.Fatal("the converger never reached equilibrium")
	}

	cancel()

	if err := <-runErr; err != nil {
		t.Fatalf("Run = %v, want nil on cancellation", err)
	}

	_, applied := h.snapshot()

	if want := []string{"NETNS_CREATE", "UNIT_START"}; !slices.Equal(applied, want) {
		t.Fatalf("applied = %v, want %v", applied, want)
	}
}

func TestConvergerAppliesOpcodesThroughATable(t *testing.T) {
	// The velocity-native effect shape: the plan emits Instructions, the
	// table is their only executor, a Runner bounds the batch, and a shared
	// dedupe group coalesces repeats.
	const (
		opNetnsCreate opcodes.Op = 1
		opUnitStart   opcodes.Op = 2
	)

	h := &podHarness{world: pod{wantNet: true, wantUnit: "running"}}

	table := opruntime.NewTable()

	for _, reg := range []struct {
		op   opcodes.Op
		eff  string
		want func(*pod)
	}{
		{opNetnsCreate, "NETNS_CREATE", func(p *pod) { p.hasNet = true }},
		{opUnitStart, "UNIT_START", func(p *pod) { p.unitState = "running" }},
	} {
		eff := reg.eff

		if err := table.Register(reg.op, func(opcodes.Instruction) error {
			return h.apply(context.Background(), eff)
		}); err != nil {
			t.Fatal(err)
		}
	}

	opPlan := []teleos.Invariant[pod, opcodes.Instruction]{
		teleos.Align(
			func(w pod) bool { return w.wantNet },
			func(w pod) bool { return w.hasNet },
			func(target bool, _ pod) opcodes.Instruction {
				if target {
					return opcodes.Instruction{Op: opNetnsCreate}
				}

				return opcodes.Instruction{Op: opcodes.OpNop}
			},
		),
		teleos.Align(
			func(w pod) string { return w.wantUnit },
			func(w pod) string { return w.unitState },
			func(target string, _ pod) opcodes.Instruction {
				if target == "running" {
					return opcodes.Instruction{Op: opUnitStart}
				}

				return opcodes.Instruction{Op: opcodes.OpNop}
			},
		),
	}

	engine, err := teleos.New(teleos.Config[pod, opcodes.Instruction]{
		Plan:      opPlan,
		EffectKey: converge.InstructionKey,
	})
	if err != nil {
		t.Fatal(err)
	}

	group, gerr := dedupe.NewSingleflight[string, struct{}]()
	if gerr != nil {
		t.Fatal(gerr)
	}

	runner, rerr := async.New(async.Limited(2))
	if rerr != nil {
		t.Fatal(rerr)
	}

	converged := make(chan struct{}, 1)

	c, err := converge.New(engine, h.observe, converge.Dispatch(table), converge.Config[pod, opcodes.Instruction]{
		Runner:   runner,
		Coalesce: group,
		Key:      converge.InstructionKey,
		OnReport: func(_ context.Context, r teleos.Report[pod, opcodes.Instruction]) {
			if r.Status == teleos.Converged {
				select {
				case converged <- struct{}{}:
				default:
				}
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go c.Run(ctx)

	c.Wake()

	select {
	case <-converged:
	case <-time.After(5 * time.Second):
		t.Fatal("the opcode plan never converged")
	}

	world, applied := h.snapshot()

	if !world.hasNet || world.unitState != "running" {
		t.Fatalf("world = %+v, want the net created and the unit running through the table", world)
	}

	if want := []string{"NETNS_CREATE", "UNIT_START"}; !slices.Equal(applied, want) {
		t.Fatalf("applied = %v, want %v through the dispatch table", applied, want)
	}
}

func TestConvergerObservationFailureIsTransient(t *testing.T) {
	var (
		calls   atomic.Int32
		onError = make(chan error, 1)
	)

	engine, err := teleos.New(teleos.Config[pod, string]{Plan: podInvariants()})
	if err != nil {
		t.Fatal(err)
	}

	h := &podHarness{world: pod{wantNet: true, wantUnit: "running"}}

	converged := make(chan struct{}, 1)

	c, err := converge.New(
		engine,
		func(context.Context) (pod, error) {
			if calls.Add(1) == 2 {
				return pod{}, errors.New("dbus hiccup")
			}

			return h.observe(context.Background())
		},
		h.apply,
		converge.Config[pod, string]{
			OnError: func(err error) { onError <- err },
			OnReport: func(_ context.Context, r teleos.Report[pod, string]) {
				if r.Status == teleos.Converged {
					select {
					case converged <- struct{}{}:
					default:
					}
				}
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go c.Run(ctx)

	// First wake: pass one observes fine and applies; pass two's observation
	// fails. The loop surfaces it and waits — alive, not dead.
	c.Wake()

	select {
	case err := <-onError:
		if err.Error() != "dbus hiccup" {
			t.Fatalf("onError = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the observation failure never surfaced")
	}

	// Second wake: everything is re-derived from the next observation.
	c.Wake()

	select {
	case <-converged:
	case <-time.After(5 * time.Second):
		t.Fatal("the converger never recovered from the failed observation")
	}
}

func TestConvergerAppliesInOrderUnderARunner(t *testing.T) {
	var (
		mu    sync.Mutex
		order []string
		cur   atomic.Int32
		peak  atomic.Int32
	)

	engine, err := teleos.New(teleos.Config[struct{}, string]{
		Plan: []teleos.Invariant[struct{}, string]{
			teleos.Rule(
				func(struct{}) bool { return false },
				func(struct{}) []string { return []string{"first", "second", "third"} },
			),
		},
		MaxPasses: 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	runner, rerr := async.New(async.Limited(1))
	if rerr != nil {
		t.Fatal(rerr)
	}

	applied := make(chan string, 8)

	apply := func(_ context.Context, eff string) error {
		n := cur.Add(1)
		if p := peak.Load(); n > p {
			peak.Store(n)
		}

		mu.Lock()

		order = append(order, eff)
		mu.Unlock()

		applied <- eff

		cur.Add(-1)

		return nil
	}

	c, err := converge.New(
		engine,
		func(context.Context) (struct{}, error) {
			return struct{}{}, nil
		},
		apply,
		converge.Config[struct{}, string]{
			Runner:  runner,
			OnError: func(error) {},
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go c.Run(ctx)

	c.Wake()

	// Three effects of the single pass, applied through a Limited(1) runner.
	for range 3 {
		select {
		case <-applied:
		case <-time.After(5 * time.Second):
			t.Fatal("the runner never applied the want list")
		}
	}

	mu.Lock()
	defer mu.Unlock()

	if want := []string{"first", "second", "third"}; !slices.Equal(order, want) {
		t.Fatalf("order = %v, want %v", order, want)
	}

	if peak.Load() != 1 {
		t.Fatalf("peak concurrency = %d, want 1 under Limited(1)", peak.Load())
	}
}
