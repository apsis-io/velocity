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
// open, so creation, verification, repair, and teardown are one list read the
// same way every pass.
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
//   - Effects re-emit while done is false. Reality may take many passes to
//     catch up — a unit that is activating, a mount that is settling — so the
//     executor either applies an effect idempotently or coalesces identical
//     in-flight effects by key. velocity's dedupe.Group keyed on the effect
//     identity is the coalescing shape.
//   - Observation cadence belongs to the daemon. A poll period or a watch
//     wakeup is the caller's decision; nothing here measures time, because a
//     budget that counted seconds would put a clock back into a clock-free
//     model. Every bound the engine enforces is counted in operations.
//   - An effect that fails is re-derived by the next pass, not retried by this
//     one. The engine's budgets — per-pass effect cap, pass cap, stall and
//     oscillation detection — are what turn an infinite re-emit into a
//     diagnosed terminal status the daemon can log, alarm on, and Reset past
//     once a human has intervened.
package teleos
