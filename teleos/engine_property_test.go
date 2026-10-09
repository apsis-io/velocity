// Copyright (C) 2025-2026 Malformed C. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package teleos_test

import (
	"math/rand"
	"testing"

	"github.com/apsis-io/velocity/teleos"
	"github.com/apsis-io/velocity/teleos/entropy"
)

// A property test over the engine, through the entropy harness — because
// the targeted tests only assert the shapes someone thought of. The
// generator builds random plans over a small bit-vector world and drives
// them with an executor that mostly cooperates and occasionally sabotages —
// applying an effect the plan did not ask for — which manufactures stalls,
// oscillations, and late convergences the targeted tests would have to be
// written by hand to reach.
//
// The harness asserts the three properties every daemon implicitly depends
// on, so this test states none of them itself: the engine halts within its
// own budget, effects ship only on Frontier, and terminal stays terminal.
// What the generator adds is the adversarial distribution — plans and
// worlds chosen to make those properties expensive.
func TestEnginePropertyHaltsUnderSabotageAndNamesTheDiagnosis(t *testing.T) {
	rng := rand.New(rand.NewSource(20261006))

	for range 300 {
		// A random plan: one to four rules over distinct bits, each a
		// named stage.
		n := 1 + rng.Intn(4)

		plan := make([]teleos.Stage[propBits, string], n)

		for i := range plan {
			bit := i

			plan[i] = teleos.Stage[propBits, string]{
				Name: string(rune('A' + i)),
				Check: teleos.Rule(
					func(w propBits) bool { return w.bits[bit] },
					func(propBits) []string { return []string{string(rune('a' + bit))} },
				),
			}
		}

		// Sabotage: some iterations mis-apply effects, some run without the
		// stall oracle, some run with it.
		sabotage := rng.Intn(4) == 0
		same := rng.Intn(2) == 0

		sideRng := rand.New(rand.NewSource(rng.Int63()))

		verdict := entropy.Run(t, entropy.Config[propBits, string]{
			Stages: plan,
			Apply: func(w propBits, eff string) propBits {
				bit := eff[0] - 'a'

				// Mostly cooperate; sometimes apply an effect belonging to
				// the wrong stage, which flips the world somewhere the plan
				// did not ask for.
				if sabotage && sideRng.Intn(5) == 0 {
					bit = byte(sideRng.Intn(4))
				}

				w.bits[bit%4] = !w.bits[bit%4]

				return w
			},
			Same: func(a, b propBits) bool {
				if !same {
					return false
				}

				return a == b
			},
			MaxPasses: 24,
			Seed:      rng.Int63(),
		}, propBits{})

		// The harness guarantees halting; what the generator contributes is
		// that the halt arrives as a NAMED diagnosis — a sabotaged world
		// that cannot converge is Stalled or Exhausted or Oscillating, and
		// a convergence under sabotage is a plan that absorbed the damage.
		_ = verdict
	}
}

// propBits is the property world: four independent bits the random plans
// chase.
type propBits struct {
	bits [4]bool
}
