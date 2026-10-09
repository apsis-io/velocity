// Copyright (C) 2025-2026 Malformed C. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package teleos_test

import (
	"fmt"
	"math/rand"
	"testing"

	"github.com/apsis-io/velocity/teleos"
	"github.com/apsis-io/velocity/teleos/entropy"
)

// TestChainPropertyNeverRemovesWhatIsStoodOn is the release sweep's
// guarantee stated as a property over random worlds: the chain never
// removes a link while a link stands above it (present), and never
// acquires a link while the link beneath it is absent. Intents are drawn
// prefix-shaped — the links that should exist form a prefix of the chain —
// because that is what a satisfiable chain intent is; contradictory intent
// is the documented unresolvable class, and no ordering property can hold
// for it. Three hundred random worlds, each driven to rest by a faithful
// executor, through the entropy harness: the order property is the Audit,
// the convergence is the harness's.
func TestChainPropertyNeverRemovesWhatIsStoodOn(t *testing.T) {
	rng := rand.New(rand.NewSource(6102026))

	for iter := range 300 {
		var observed [3]bool

		for i := range observed {
			observed[i] = rng.Intn(2) == 0
		}

		// Prefix intent: the first k links wanted present, the rest gone.
		wanted := rng.Intn(4)

		var desired [3]bool

		for i := range desired {
			desired[i] = i < wanted
		}

		link := func(i int) teleos.Resource[[3]bool, string] {
			return teleos.Resource[[3]bool, string]{
				Exists: func(w [3]bool) bool { return w[i] },
				Invariant: teleos.Align(
					func([3]bool) bool { return desired[i] },
					func(w [3]bool) bool { return w[i] },
					func(target bool, _ [3]bool) string {
						if target {
							return "A" + string(rune('0'+i))
						}

						return "D" + string(rune('0'+i))
					},
				),
			}
		}

		chain := teleos.Chain(link(0), link(1), link(2))

		v := entropy.Run(t, entropy.Config[[3]bool, string]{
			Stages: []teleos.Stage[[3]bool, string]{{Check: chain}},
			Apply: func(w [3]bool, eff string) [3]bool {
				idx := int(eff[1] - '0')
				w[idx] = eff[0] == 'A'

				return w
			},
			Audit: func(_ int, w [3]bool, want []string) error {
				for _, eff := range want {
					removing := eff[0] == 'D'
					idx := int(eff[1] - '0')

					for j := range 3 {
						switch {
						case removing && j > idx && w[j]:
							return fmt.Errorf("removing link %d while link %d stands on it", idx, j)
						case !removing && j < idx && !w[j]:
							return fmt.Errorf("acquiring link %d while link %d beneath it is absent", idx, j)
						}
					}
				}

				return nil
			},
			MaxPasses: 12,
		}, observed)

		if !v.Converged {
			t.Fatalf("iter %d: verdict = %+v, want convergence under a faithful executor", iter, v)
		}
	}
}
