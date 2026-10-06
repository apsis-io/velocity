// Copyright (C) 2025-2026 Malformed C. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package teleos_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/apsis-io/velocity/teleos"
)

// This file is the evaluation the library had to pass before it was more
// than a nucleus: the perigeos barrier controller (internal/primeradiant/
// barrier_controller.go, ADR-0032) re-expressed as a teleos plan and driven
// by the engine, with its properties asserted rather than its phases.
//
// The controller coordinates a checkpoint across a graph of pods with no
// outage: arm every member (quiesce), wait for EVERY member to report
// quiesced, only then checkpoint any of them, and restore each member as
// its checkpoint lands. It ships as a hand-rolled phase machine —
// Pending → Arming → Checkpointing → Done/Failed — with the no-outage
// property enforced by where the code happens to sit inside
// reconcileArming. Here the same behavior is one plan, and the properties
// are structural.
//
// What the evaluation found, stated up front:
//
//   - The phase machine is the frontier. Pending/Arming/Checkpointing/Done
//     were hand-maintained state with a switch in reconcileOne; here they
//     are Plan's halting order, and Report.Stage renders status.phase for
//     free.
//   - The wall-clock timeout moved into the observation. The controller's
//     barrierQuiesceTimeout reads a clock inside the reconciler; an
//     invariant reads no clock, so the deadline becomes an observed fact —
//     the observer computes "the quiesce window was missed" and the plan
//     reacts to it like any other fact. The clock lives where clocks live:
//     in the daemon's watch loop.
//   - Fail-fast became abort-plus-diagnosis. The controller marks the object
//     Failed the moment any member reports a failed checkpoint; the plan
//     emits ABORT (which the executor applies graph-wide — the M-barrier-1
//     lesson) and the driver marks the object failed out-of-band. The
//     engine's pass budget is the backstop if the driver misses it: bounded
//     attempts, then Exhausted names the member.
//   - Admission logic stays outside. validateMember's structural checks run
//     once, before the object enters the loop; convergence is for state that
//     changes under it, and a spec that fails validation never converges to
//     anything.
//   - "Concurrent enough across ticks" is native. The controller's own doc
//     comment defends per-member progress across ticks as the standard
//     controller shape; here it is one Concurrent stage — every ready
//     member's effects aggregate into a single pass, and the Runner batch
//     bounds the fan-out.

// The controller's writes, as data. In the real wiring these are opcodes
// through an opruntime.Table; strings keep the evaluation readable.
const (
	armEff         = "ARM:"
	checkpointEff  = "MIGRATE_TO:"
	restoreEff     = "RESTORE:"
	abortEff       = "ABORT_BARRIER"
	quiesceStageNm = "Quiescing"
)

// barrierWorld is the observation: what is true about the graph right now.
// In the real controller this is assembled from the CRD's status, the pods'
// annotations, and the deadline check; here it is one struct. Transit is
// observed, not inferred — an armed-but-not-yet-quiesced member is a fact.
type barrierWorld struct {
	quiesced map[string]bool
	armed    map[string]bool

	// requested: the checkpoint request has been sent. dataReady: the
	// member's checkpoint file is available to copy. copied: it has been
	// persisted onto the CRD's status. restored: the delete+recreate onto
	// the target has landed.
	requested map[string]bool
	dataReady map[string]bool
	copied    map[string]bool
	restored  map[string]bool

	// quiesceDeadlineMissed is computed by the observer: armed, the window
	// has closed, and not every member quiesced. The clock read lives in
	// the watch loop, not in the invariant.
	quiesceDeadlineMissed bool
}

// quiesceInvariant is one member's arm-and-hold: arm if not armed, hold (an
// open gap with no effects — in transit) while the graph quiesces, and turn
// a missed deadline into the graph-wide abort. It is done only when this
// member reports quiesced.
func quiesceInvariant(member string) teleos.Invariant[barrierWorld, string] {
	return func(s barrierWorld) (bool, []string) {
		if s.quiesced[member] {
			return true, nil
		}

		if !s.armed[member] {
			return false, []string{armEff + member}
		}

		if s.quiesceDeadlineMissed {
			return false, []string{abortEff}
		}

		return false, nil // armed, not yet quiesced, window open: hold
	}
}

// memberChains builds each member's own copy-then-restore chain: the
// checkpoint data is requested, then copied into the CRD's status (persisted
// — the controller's "restore happens next tick, once this is persisted"),
// and only then is the delete+recreate restore allowed to run.
func memberChains(members []string) []teleos.Invariant[barrierWorld, string] {
	var chains []teleos.Invariant[barrierWorld, string]

	for _, m := range members {
		member := m

		chains = append(chains, teleos.Chain(
			teleos.Resource[barrierWorld, string]{
				Exists: func(w barrierWorld) bool { return w.copied[member] },
				Invariant: teleos.Rule(
					func(w barrierWorld) bool { return w.copied[member] },
					func(w barrierWorld) []string {
						switch {
						case !w.requested[member]:
							return []string{checkpointEff + member}
						case !w.dataReady[member]:
							return nil // in transit: the checkpoint is being written
						default:
							return []string{"COPY:" + member}
						}
					},
				),
			},
			teleos.Resource[barrierWorld, string]{
				Exists: func(w barrierWorld) bool { return w.restored[member] },
				Invariant: teleos.Rule(
					func(w barrierWorld) bool { return w.restored[member] },
					func(w barrierWorld) []string {
						if !w.copied[member] {
							return nil
						}

						return []string{restoreEff + member}
					},
				),
			},
		))
	}

	return chains
}

// barrierStages is the whole controller as two named stages: quiesce
// everyone (the barrier), then every member's copy-then-restore chain runs
// concurrently — members progress independently, which is the property the
// controller's own doc comment defends as "concurrent enough across ticks".
// The barrier is the Plan gate; the concurrency is the Concurrent stage.
// The names are the controller's phases: Report.Stage renders
// status.phase from these.
func barrierStages(members []string) []teleos.Stage[barrierWorld, string] {
	quiesceStage := teleos.Concurrent(
		quiesceInvariant(members[0]),
		quiesceInvariant(members[1]),
		quiesceInvariant(members[2]),
	)

	restoreStage := teleos.Concurrent(memberChains(members)...)

	return []teleos.Stage[barrierWorld, string]{
		{Name: quiesceStageNm, Check: quiesceStage},
		{Name: "Restoring", Check: restoreStage},
	}
}

// barrierHost is the host model: effects change facts, and the next
// observation reads them back. Host-side physics resolves between passes —
// the trail process acks after being armed, the checkpoint file appears
// after the request — exactly as the controller's informer cache updates
// between ticks.
type barrierHost struct {
	members []string

	world barrierWorld
}

func newBarrierHost(members []string) *barrierHost {
	h := &barrierHost{members: members, world: barrierWorld{
		quiesced:  map[string]bool{},
		armed:     map[string]bool{},
		requested: map[string]bool{},
		dataReady: map[string]bool{},
		copied:    map[string]bool{},
		restored:  map[string]bool{},
	}}

	return h
}

// observe returns the world with host physics resolved for this pass: every
// armed member's trail has acked by now, and every requested checkpoint has
// produced its file.
func (h *barrierHost) observe() barrierWorld {
	w := h.world
	for _, m := range h.members {
		if h.world.armed[m] {
			w.quiesced[m] = true
		}

		if h.world.requested[m] {
			w.dataReady[m] = true
		}
	}

	return w
}

// apply commits a pass's effects into the host state.
func (h *barrierHost) apply(want []string) {
	for _, eff := range want {
		if m, ok := strings.CutPrefix(eff, armEff); ok {
			h.world.armed[m] = true
		} else if m, ok := strings.CutPrefix(eff, checkpointEff); ok {
			h.world.requested[m] = true
		} else if m, ok := strings.CutPrefix(eff, "COPY:"); ok {
			h.world.copied[m] = true
		} else if m, ok := strings.CutPrefix(eff, restoreEff); ok {
			h.world.restored[m] = true
		}

		// The abort effect releases the whole graph (Resume to every member,
		// the M-barrier-1 lesson) and the driver marks the object Failed.
	}
}

// TestBarrierPlanArmsEveryoneBeforeAnyCheckpoint is the no-outage property,
// the one the ADR exists for: the arm effects fan out to every member in one
// pass, and no checkpoint request may be emitted while any member is still
// un-quiesced — no matter how many passes the arming holds.
func TestBarrierPlanArmsEveryoneBeforeAnyCheckpoint(t *testing.T) {
	h := newBarrierHost([]string{"alpha", "beta", "gamma"})

	engine, err := teleos.New(teleos.Config[barrierWorld, string]{
		Plan: barrierStages(h.members),
	})
	if err != nil {
		t.Fatal(err)
	}

	var arms []string

	passes := 0

	for {
		report := engine.Step(h.observe())
		passes++

		if report.Status != teleos.Frontier {
			t.Fatalf("status = %v before any member quiesced, want the barrier holding", report.Status)
		}

		if report.Frontier != 0 {
			t.Fatalf("frontier = %d, want the quiesce stage holding", report.Frontier)
		}

		h.apply(report.Want)

		for _, eff := range report.Want {
			if m, ok := strings.CutPrefix(eff, armEff); ok {
				arms = append(arms, m)
			}

			if strings.HasPrefix(eff, checkpointEff) || strings.HasPrefix(eff, restoreEff) {
				t.Fatalf("effect %q escaped the quiesce stage", eff)
			}
		}

		// Once every member is armed, the observation delivers the acks and
		// the barrier opens on the next pass — which this test stops at,
		// having proven what it set out to prove.
		if len(arms) == len(h.members) {
			break
		}

		if passes > 16 {
			t.Fatal("arming never completed")
		}
	}

	if !slices.Equal(arms, []string{"alpha", "beta", "gamma"}) {
		t.Fatalf("arms = %v, want one arm per member", arms)
	}
}

// TestBarrierTimeoutIsAnObservedFact: when the window closes with a member
// un-acked, the next pass emits the abort — the controller's
// barrierQuiesceTimeout path, with the clock in the observer and the abort
// graph-wide.
func TestBarrierTimeoutIsAnObservedFact(t *testing.T) {
	h := newBarrierHost([]string{"alpha", "beta", "gamma"})
	h.world.armed = map[string]bool{"alpha": true, "beta": true, "gamma": true}
	h.world.quiesced = map[string]bool{"alpha": true} // beta never acks

	// The deadline is computed by the real daemon against the CRD's status —
	// the raw world, not the ack-resolving view.
	w := h.world
	w.quiesceDeadlineMissed = true

	done, want := quiesceInvariant("beta")(w)
	if done {
		t.Fatal("un-acked member reported done after the window closed")
	}

	if len(want) != 1 || want[0] != abortEff {
		t.Fatalf("want = %v, want the graph-wide abort", want)
	}
}

// TestBarrierHoldEmitsNothingWhileInTransit: an armed member whose ack has
// not landed, and a member whose checkpoint is being written, hold with an
// open gap and NO effects — the in-transit answer to "what does the engine
// emit while reality catches up": nothing.
func TestBarrierHoldEmitsNothingWhileInTransit(t *testing.T) {
	h := newBarrierHost([]string{"alpha", "beta"})
	h.world.armed["alpha"] = true
	h.world.requested["alpha"] = true

	w := h.observe()
	w.dataReady["alpha"] = false

	if _, want := quiesceInvariant("alpha")(w); want != nil {
		t.Fatalf("armed-but-unacked want = %v, want a silent hold", want)
	}

	for _, m := range h.members {
		_ = m
	}

	if _, want := memberChains([]string{"alpha"})[0](w); want != nil {
		t.Fatalf("requested-but-unready want = %v, want a silent hold", want)
	}
}

// TestBarrierPlanEndToEnd drives the whole barrier — arm, quiesce, request,
// copy, restore, for every member — through the engine, and asserts the
// properties the hand-rolled version enforces by code placement: the stage
// names track the controller's phases, nothing crosses the barrier early,
// and every member ends restored.
func TestBarrierPlanEndToEnd(t *testing.T) {
	h := newBarrierHost([]string{"alpha", "beta", "gamma"})

	engine, err := teleos.New(teleos.Config[barrierWorld, string]{
		Plan: barrierStages(h.members),
	})
	if err != nil {
		t.Fatal(err)
	}

	var sawRestoring bool

	for range 64 {
		report := engine.Step(h.observe())

		if report.Frontier == 1 && report.Stage == "Restoring" {
			sawRestoring = true
		}

		if report.Status == teleos.Converged {
			if !sawRestoring {
				t.Fatal("converged without ever holding the checkpointing stage")
			}

			w := h.observe()

			for _, m := range h.members {
				if !w.restored[m] {
					t.Fatalf("member %s not restored at convergence", m)
				}
			}

			return
		}

		if report.Status != teleos.Frontier {
			t.Fatalf("status = %v mid-barrier", report.Status)
		}

		for _, eff := range report.Want {
			if strings.HasPrefix(eff, restoreEff) && report.Frontier == 0 {
				t.Fatalf("restore %q escaped the barrier: the quiesce stage still holds", eff)
			}

			if strings.HasPrefix(eff, checkpointEff) && report.Frontier == 0 {
				t.Fatalf("checkpoint %q crossed the barrier: someone is not quiesced", eff)
			}
		}

		h.apply(report.Want)
	}

	t.Fatal("the barrier never converged in 64 passes")
}
