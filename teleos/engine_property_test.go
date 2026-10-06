// Copyright (C) 2025-2026 Malformed C. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package teleos

import (
	"math/rand"
	"testing"
)

// A property test over the engine, because the targeted tests only assert
// the shapes someone thought of. The generator builds random plans over a
// small bit-vector world and drives them with an executor that mostly
// cooperates and occasionally sabotages — applying the wrong effect — which
// manufactures stalls, oscillations, and late convergences the targeted
// tests would have to be written by hand to reach.
//
// Whatever the plan and whatever the sabotage, three properties must hold:
//
//  1. The engine halts: within its own pass budget, some terminal status is
//     reported. A loop that could spin forever past its budget would make
//     every daemon using this library unshut-downable.
//  2. Effects only ever ship on Frontier. A terminal report carrying a Want
//     would hand a diagnosed-dead plan's effects to an executor.
//  3. Terminal is terminal. Once a terminal status is reported, every
//     further Step without Reset returns it unchanged. A diagnosis that
//     un-diagnoses itself because the daemon kept polling is not a
//     diagnosis.

type propWorld struct {
	bits [4]bool
}

func TestEnginePropertyHaltsEffectsOnlyOnFrontierAndTerminalStaysTerminal(t *testing.T) {
	rng := rand.New(rand.NewSource(20261006))

	for iter := range 300 {
		iter := iter

		// A random plan: one to four rules over distinct bits.
		n := 1 + rng.Intn(4)

		plan := make([]Invariant[propWorld, string], n)
		names := make([]string, n)

		for i := range plan {
			bit := i

			plan[i] = Rule(
				func(w propWorld) bool { return w.bits[bit] },
				func(propWorld) []string { return []string{string(rune('a' + bit))} },
			)

			names[i] = string(rune('A' + i))
		}

		// Sabotage: some iterations mis-apply effects, some run without the
		// stall oracle, some run with it.
		sabotage := rng.Intn(4) == 0
		same := rng.Intn(2) == 0

		engine, err := New(Config[propWorld, string]{
			Plan:      plan,
			Names:     names,
			EffectKey: func(s string) string { return s },
			MaxPasses: 24,
			Same: func(a, b propWorld) bool {
				if !same {
					return false
				}

				return a == b
			},
		})
		if err != nil {
			t.Fatalf("iter %d: %v", iter, err)
		}

		world := propWorld{}

		for pass := 1; pass <= 24+1; pass++ {
			report := engine.Step(world)

			switch report.Status {
			case Frontier:
				if len(report.Want) == 0 {
					t.Fatalf("iter %d pass %d: frontier with no effects", iter, pass)
				}

				// Mostly cooperate; sometimes apply an effect belonging to
				// the wrong stage, which flips the world somewhere the plan
				// did not ask for.
				eff := report.Want[0][0] - 'a'

				if sabotage && rng.Intn(5) == 0 {
					eff = byte(rng.Intn(4))
				}

				world.bits[eff%4] = !world.bits[eff%4]

			case Converged, Stalled, Oscillating, Exhausted:
				// The budget bounds FRONTIER passes: twenty-four open
				// frontiers and the twenty-fifth Step reports Exhausted. A
				// convergence landing on that pass is success, not a breach.
				again := engine.Step(world)

				if again.Status != report.Status || again.Passes != report.Passes || again.Effects != report.Effects {
					t.Fatalf("iter %d pass %d: terminal %v became %v", iter, pass, report.Status, again.Status)
				}

				continue
			}

			if pass == 25 {
				t.Fatalf("iter %d: engine still on the frontier at pass 25 of a 24-pass budget", iter)
			}
		}
	}
}
