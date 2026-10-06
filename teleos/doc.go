// Copyright (C) 2025-2026 Malformed C. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package teleos is level-triggered invariant convergence: a system is
// described by the resting state it must hold, not by the steps that get it
// there.
//
// The atom is a pure function over an observation:
//
//	invariant := func(state S) (done bool, want []E)
//
// done says whether this slice of reality already satisfies equilibrium; want
// is the minimal list of data effects that would nudge it there. An invariant
// performs no I/O, acquires no lock, and reads no clock — it is amnesiac,
// answering from the present observation alone. Because the evaluation is
// pure, crash recovery is the same evaluation as steady state, an externally
// broken resource re-opens its gap on the next pass, and tests assert
// (done, want) against a struct with nothing mocked.
//
// Plan composes invariants in slice order and halts at the first unsatisfied
// one — the convergence frontier. Slice order is dependency order: a later
// invariant is neither evaluated nor satisfied while an earlier one stands
// open, so a later stage never acts on an unready foundation. Teardown is the
// exception that proves the order — it is the topological inverse of
// bringup — and it needs no phase flag: Chain composes resources whose links
// acquire root to leaf and release leaf to root, deriving the direction of
// every pass from the chain itself. A forward sweep over the bringup list
// during teardown deletes the netns the process still lives in; the chain
// stops the process first, because a link may not remove itself while
// anything stands on it. Concurrent composes without order, for stages that
// are genuinely independent.
//
// Stages can be named, and the report is then a status document: Stage says
// which stage holds the frontier, and Names[:Frontier] is what has already
// converged. Kubernetes PodConditions, per-stage metrics, and progress
// output are projections of that one report.
//
// The Engine is the pass machine over a plan. It is deliberately inert: it
// never observes the world, never sleeps, and never spawns. The caller owns
// the loop:
//
//	engine := teleos.New[World, Effect](teleos.Config[World, Effect]{
//	    Plan: []teleos.Invariant[World, Effect]{netInvariant, unitInvariant},
//	})
//	for {
//	    report := engine.Step(observe()) // observe: reality into S
//	    if report.Status != teleos.Frontier {
//	        break // converged, or the engine has a diagnosis
//	    }
//	    apply(report.Want) // effects into the world; then observe again
//	}
//
// Three obligations make the contract work, and none of them live in this
// package:
//
//   - Transit is observed, not modeled. The engine is amnesiac on purpose: it
//     cannot tell "broken" from "in flight", and a third atom state would
//     reintroduce memory through the back door and break the property that
//     crash recovery is the same evaluation as steady state. So S carries
//     transit — a unit that is activating, a job queued to systemd, a mount
//     still settling — and effects that move slowly REQUIRE the executor to
//     coalesce identical in-flight effects by key (velocity's dedupe.Group)
//     or be idempotent. Whether the system reports Pending or broken is the
//     daemon's projection of S, not an engine status.
//   - Observation cadence belongs to the daemon. A poll period or a watch
//     wakeup is the caller's decision; nothing here measures time, because a
//     budget that counted seconds would put a clock back into a clock-free
//     model. Every bound the engine enforces is counted in operations.
//   - An effect that fails is re-derived by the next pass, not retried by this
//     one, and the failure path is already a circuit breaker — its unit is
//     wakes and passes, not milliseconds. One failing effect ends the pass
//     (converge waits for the next wake rather than spinning), the engine's
//     pass budget caps total attempts at MaxPasses, and Exhausted then halts
//     every emission until a human Resets. The stuck frontier is named, and
//     converge's OnError carried each failure on the way. Cost is bounded at
//     MaxPasses attempts, worst case, then silence — with no clock needed to
//     make it so.
package teleos
