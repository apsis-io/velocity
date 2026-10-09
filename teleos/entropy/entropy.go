// Copyright (C) 2025-2026 Malformed C. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package entropy is the test harness for the library's central claim: that
// a plan under the engine converges against anything the world does to it.
//
// Everything that pushes a running system away from its declared rest is
// entropy: drift (a value moves off its target), loss (a resource
// disappears), erasure (an effect wipes the facts of earlier effects — the
// un-arm), contention (another actor converging the same world), and
// sabotage (an executor that misapplies). The ENGINE is what fights it: it
// measures the gap between the declared rest and the observation, exerts the
// restoring forces the plan's invariants emit, and bounds the fight —
// sweeps, holds, budgets, stall and oscillation diagnoses. The plan does not
// fight entropy; it defines what victory means. This package is the
// sparring partner: it generates entropy, and asserts the engine wins.
//
// The harness runs a plan against a world under an entropy function and
// asserts the antientropy invariants every plan must satisfy, so a plan
// author writes the world, the executor, and the domain audit — and the
// guards are checked for them:
//
//   - the engine halts: within its own budget, a terminal status or
//     convergence, never a spin;
//   - effects ship only on Frontier — a held world gets nothing;
//   - at rest, nothing is emitted (quiescence);
//   - terminal stays terminal;
//   - and every invariant the Audit hook states holds on every pass.
//
// Deterministic by seed: the same Config and world produce the same run,
// so a failure is a reproducible case, not a flake.
package entropy

import (
	"math/rand"
	"testing"

	"github.com/apsis-io/velocity/teleos"
)

// Config builds one harness run. Apply is the executor as a pure function —
// effect into world — because a pure harness cannot flake; Entropy is the
// world's own divergence, applied after each pass's effects; Audit is where
// domain invariants live (a Chain's sweep order, a barrier's no-crossing).
type Config[S, E any] struct {
	Stages []teleos.Stage[S, E]

	// Apply applies one effect to the world and returns the new world. It
	// should be the effect's honest meaning; entropy is what makes it lie.
	Apply func(w S, e E) S

	// Entropy perturbs the world after each applied pass — drift, erasure,
	// contention. Deterministic on the harness's rng. Nil means a faithful
	// world that only changes when effects land.
	Entropy func(pass int, w S, rng *rand.Rand) S

	// Same enables stall detection, exactly as teleos.Config.Same. Without
	// it a held world is Frontier forever by design, and the harness will
	// run it to the budget.
	Same func(a, b S) bool

	// Audit states the plan's own invariants: called on every pass with the
	// world and the effects about to be applied. An error fails the run —
	// the harness's report is the diagnosis, the audit's is the proof.
	Audit func(pass int, w S, want []E) error

	// MaxEffects and MaxPasses pass through to the engine. Defaults match.
	MaxEffects int
	MaxPasses  int

	// Seed drives the harness's rng. The same seed, config, and world
	// produce the same run.
	Seed int64
}

// Verdict is what one run produced: whether the plan pulled its world to
// rest, and if not, the diagnosis the engine reached trying.
type Verdict[S any] struct {
	Converged bool
	Status    teleos.Status
	Passes    int
	Effects   int
	Final     S
}

// Run drives the plan against the world under the config's entropy, applying
// effects through Apply, and asserts the antientropy invariants as it goes.
// It returns when the engine converges or reaches a terminal diagnosis —
// never on a spin, because a spin is the first thing the harness exists to
// catch.
func Run[S, E any](t *testing.T, cfg Config[S, E], world S) Verdict[S] {
	if t == nil {
		panic("entropy: Run requires a non-nil *testing.T — it is a test harness, not a runtime loop")
	}

	t.Helper()

	engine, err := teleos.New(teleos.Config[S, E]{
		Plan:       cfg.Stages,
		Same:       cfg.Same,
		MaxEffects: cfg.MaxEffects,
		MaxPasses:  cfg.MaxPasses,
	})
	if err != nil {
		t.Fatalf("entropy: %v", err)
	}

	rng := rand.New(rand.NewSource(cfg.Seed))

	pass := 0

	for {
		pass++

		if cfg.MaxPasses > 0 && pass > cfg.MaxPasses+1 {
			t.Fatalf("entropy: still stepping at pass %d of a %d-pass budget — a spin is the first thing this harness exists to catch", pass, cfg.MaxPasses)
		}

		report := engine.Step(world)

		if report.Status == teleos.Frontier {
			if len(report.Want) == 0 {
				t.Fatalf("entropy: pass %d on the frontier with no effects — a hold is an open gap, not an empty batch", pass)
			}

			if cfg.Audit != nil {
				if err := cfg.Audit(pass, world, report.Want); err != nil {
					t.Fatalf("entropy: pass %d audit: %v", pass, err)
				}
			}

			for _, eff := range report.Want {
				world = cfg.Apply(world, eff)

				if cfg.Entropy != nil {
					world = cfg.Entropy(pass, world, rng)
				}
			}

			continue
		}

		// Terminal. One more Step must return the same diagnosis: terminal
		// is terminal.
		again := engine.Step(world)

		if again.Status != report.Status || again.Passes != report.Passes {
			t.Fatalf("entropy: terminal %v at pass %d became %v on re-Step", report.Status, pass, again.Status)
		}

		return Verdict[S]{
			Converged: report.Status == teleos.Converged,
			Status:    report.Status,
			Passes:    report.Passes,
			Effects:   report.Effects,
			Final:     world,
		}
	}
}

// Recover asserts gravitational self-healing: the plan is run to
// convergence, then drift moves the world away from rest, the engine is
// Reset, and the plan must pull the drifted world back. A plan that cannot
// survive its own success is not done.
func Recover[S, E any](t *testing.T, cfg Config[S, E], world S, drift func(S) S) Verdict[S] {
	if t == nil {
		panic("entropy: Recover requires a non-nil *testing.T — it is a test harness, not a runtime loop")
	}

	t.Helper()

	first := Run(t, cfg, world)
	if !first.Converged {
		t.Fatalf("entropy: the initial run never converged (%v)", first.Status)
	}

	cfg.Seed++ // the recovery pass gets its own entropy stream

	engine, err := teleos.New(teleos.Config[S, E]{
		Plan:       cfg.Stages,
		Same:       cfg.Same,
		MaxPasses:  cfg.MaxPasses,
		MaxEffects: cfg.MaxEffects,
	})
	if err != nil {
		t.Fatal(err)
	}

	rng := rand.New(rand.NewSource(cfg.Seed))

	drifted := world

	if drift != nil {
		drifted = drift(drifted)
	}

	if cfg.Entropy != nil {
		drifted = cfg.Entropy(first.Passes+1, drifted, rng)
	}

	engine.Reset()

	pass := 0

	for {
		pass++

		if cfg.MaxPasses > 0 && pass > cfg.MaxPasses+1 {
			t.Fatalf("entropy: the drifted world never re-converged in %d passes", cfg.MaxPasses)
		}

		report := engine.Step(drifted)

		if report.Status == teleos.Converged {
			return Verdict[S]{
				Converged: true,
				Status:    report.Status,
				Passes:    pass,
				Effects:   report.Effects,
				Final:     drifted,
			}
		}

		if report.Status != teleos.Frontier {
			t.Fatalf("entropy: the drifted world reached %v, want gravity to pull it back", report.Status)
		}

		for _, eff := range report.Want {
			drifted = cfg.Apply(drifted, eff)

			if cfg.Entropy != nil {
				drifted = cfg.Entropy(first.Passes+pass, drifted, rng)
			}
		}
	}
}
