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

func TestNewRejectsInvalidConstruction(t *testing.T) {
	engine, engErr := teleos.New(teleos.Config[pod, string]{Plan: podInvariants()})
	if engErr != nil {
		t.Fatal(engErr)
	}

	observe := func(context.Context) (pod, error) { return pod{}, nil }
	apply := func(context.Context, string) error { return nil }

	cases := []struct {
		name    string
		engine  *teleos.Engine[pod, string]
		observe func(context.Context) (pod, error)
		apply   func(context.Context, string) error
		cfg     converge.Config[pod, string]
	}{
		{"nil engine", nil, observe, apply, converge.Config[pod, string]{}},
		{"nil observe", engine, nil, apply, converge.Config[pod, string]{}},
		{"nil apply", engine, observe, nil, converge.Config[pod, string]{}},
	}

	for _, tc := range cases {
		if _, err := converge.New(tc.engine, tc.observe, tc.apply, tc.cfg); err == nil {
			t.Fatalf("%s: New accepted it", tc.name)
		}
	}

	if _, err := converge.New(engine, observe, apply, converge.Config[pod, string]{
		Coalesce: mustGroup(t),
	}); err == nil {
		t.Fatal("New accepted a coalescer without an effect key")
	}
}

func mustGroup(t *testing.T) *dedupe.Group[string, struct{}] {
	t.Helper()

	g, err := dedupe.NewSingleflight[string, struct{}]()
	if err != nil {
		t.Fatal(err)
	}

	return g
}

// TestConvergerHonorsAWakeSentBeforeRun: wakes queue, so a daemon that wakes
// during startup does not lose the reconcile.
func TestConvergerHonorsAWakeSentBeforeRun(t *testing.T) {
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

	c.Wake() // before Run

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go c.Run(ctx)

	select {
	case <-converged:
	case <-time.After(5 * time.Second):
		t.Fatal("the pre-Run wake was lost")
	}
}

// TestConvergerCollapsesAwakeStorm: wakes sent while the loop is busy
// inside a pass collapse into one pending pass. The first observe blocks on
// a gate, so the loop is provably mid-pass while the storm lands; when the
// gate opens, exactly one queued wake survives.
func TestConvergerCollapsesAwakeStorm(t *testing.T) {
	h := &podHarness{world: pod{wantNet: true, wantUnit: "running"}}

	engine, err := teleos.New(teleos.Config[pod, string]{Plan: podInvariants()})
	if err != nil {
		t.Fatal(err)
	}

	converged := make(chan struct{}, 1)

	var (
		observes atomic.Int32
		gate     = make(chan struct{})
	)

	c, err := converge.New(
		engine,
		func(context.Context) (pod, error) {
			if observes.Add(1) == 1 {
				<-gate // the loop is held mid-pass while the storm lands
			}

			return h.observe(context.Background())
		},
		h.apply,
		converge.Config[pod, string]{
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

	// The loop is now blocked inside its first observe. Land the storm.
	for range 5 {
		c.Wake()
	}

	close(gate) // release the pass; the queued wake is what it ends on

	select {
	case <-converged:
	case <-time.After(5 * time.Second):
		t.Fatal("the gated pass never converged")
	}

	// Settle: the collapsed wake may serve exactly one more pass, which
	// observes once more and finds the world already at rest.
	deadline := time.After(5 * time.Second)

	for observes.Load() < 3 {
		select {
		case <-deadline:
			t.Fatalf("observes = %d, want the gated pass plus the collapsed wake's", observes.Load())
		case <-time.After(time.Millisecond):
		}
	}

	time.Sleep(50 * time.Millisecond)

	if got := observes.Load(); got != 3 {
		t.Fatalf("observes = %d after the storm settled, want 3: the gated pass, its convergence check, and the collapsed wake's pass", got)
	}
}

// TestConvergerEffectFailureIsTransientAndRetriedOnWake: a failing effect
// ends the pass, surfaces through OnError, and the next wake re-derives it.
func TestConvergerEffectFailureIsTransientAndRetriedOnWake(t *testing.T) {
	h := &podHarness{world: pod{wantNet: true, wantUnit: "running"}}

	engine, err := teleos.New(teleos.Config[pod, string]{Plan: podInvariants()})
	if err != nil {
		t.Fatal(err)
	}

	var (
		calls   atomic.Int32
		onError = make(chan error, 1)
	)

	converged := make(chan struct{}, 1)

	c, err := converge.New(
		engine,
		h.observe,
		func(_ context.Context, eff string) error {
			switch eff {
			case "NETNS_CREATE":
				h.mu.Lock()
				h.world.hasNet = true
				h.mu.Unlock()

				return nil
			default: // UNIT_START fails on its first attempt
				if calls.Add(1) == 1 {
					return errors.New("patch rejected")
				}

				h.mu.Lock()
				h.world.unitState = "running"
				h.mu.Unlock()

				return nil
			}
		},
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

	c.Wake()

	select {
	case err := <-onError:
		if err.Error() != "patch rejected" {
			t.Fatalf("onError = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the effect failure never surfaced")
	}

	// The next wake re-derives everything; the applier no longer fails.
	c.Wake()

	select {
	case <-converged:
	case <-time.After(5 * time.Second):
		t.Fatal("the converger never recovered from the failed effect")
	}
}

// TestConvergerStallDiagnosisWaitsForReset: a terminal diagnosis stops the
// pass loop until the human (here, the test) Resets the engine and wakes it
// again — Reset on a schedule would be how a diagnosis gets ignored.
func TestConvergerStallDiagnosisWaitsForReset(t *testing.T) {
	h := &podHarness{world: pod{wantUnit: "activating"}}

	engine, err := teleos.New(teleos.Config[pod, string]{
		Plan: []teleos.Invariant[pod, string]{
			teleos.Rule(
				func(w pod) bool { return w.unitState == "running" },
				func(pod) []string { return []string{"UNIT_START"} },
			),
		},
		EffectKey: func(s string) string { return s },
		Same: func(a, b pod) bool {
			return a.unitState == b.unitState
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	var stalls atomic.Int32

	converged := make(chan struct{}, 1)

	var c *converge.Converger[pod, string]

	// The applier succeeds and reality does not move — the effect was
	// accepted by something that then ignored it, which is what a stall is.
	c, err = converge.New(engine, h.observe, func(context.Context, string) error { return nil }, converge.Config[pod, string]{
		OnReport: func(_ context.Context, r teleos.Report[pod, string]) {
			switch r.Status {
			case teleos.Stalled:
				// The human's decision: whatever held the world still has
				// been dealt with, so the diagnosis clears.
				stalls.Add(1)
				engine.Reset()
				c.Wake()
			case teleos.Converged:
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

	// The world is frozen and the unit never activates: the stall diagnosis
	// repeats until reality un-sticks.
	deadline := time.After(5 * time.Second)

	for stalls.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("the stall diagnosis never surfaced")
		case <-time.After(time.Millisecond):
		}
	}

	// Reality un-sticks while the diagnosis is being handled.
	h.mu.Lock()
	h.world.unitState = "running"
	h.mu.Unlock()

	select {
	case <-converged:
	case <-time.After(5 * time.Second):
		t.Fatal("the converger never recovered after Reset")
	}
}
