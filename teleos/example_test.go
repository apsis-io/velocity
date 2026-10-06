// Copyright (C) 2025-2026 Malformed C. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package teleos_test

import (
	"fmt"

	"github.com/apsis-io/velocity/teleos"
)

func Example_podConvergence() {
	engine, err := teleos.New(teleos.Config[World, string]{Plan: PodInvariants})
	if err != nil {
		panic(err)
	}

	world := World{WantNet: true, WantUnit: "running"}

	for pass := 1; ; pass++ {
		report := engine.Step(world)

		if report.Status == teleos.Converged {
			fmt.Printf("Pass %d: Equilibrium reached (Telos).\n", pass)

			break
		}

		fmt.Printf("Pass %d: Frontier active. Emitting: %v\n", pass, report.Want)

		for _, eff := range report.Want {
			switch eff {
			case "NETNS_CREATE":
				world.HasNet = true
			case "UNIT_START":
				world.UnitState = "running"
			}
		}
	}

	// Output:
	// Pass 1: Frontier active. Emitting: [NETNS_CREATE]
	// Pass 2: Frontier active. Emitting: [UNIT_START]
	// Pass 3: Equilibrium reached (Telos).
}
