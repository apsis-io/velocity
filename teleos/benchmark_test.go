// Copyright (C) 2025-2026 Malformed C. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package teleos

import "testing"

// The pass is the unit a daemon's loop repeats forever, so its cost at rest
// is the library's cost of running. Converged measures the steady state —
// every stage satisfied, the whole plan walked; open measures the frontier
// halting at the first stage.

type benchWorld struct {
	net  bool
	unit string
}

func benchPlan() []Invariant[benchWorld, string] {
	return []Invariant[benchWorld, string]{
		Align(
			func(w benchWorld) bool { return true },
			func(w benchWorld) bool { return w.net },
			func(bool, benchWorld) string { return "NETNS" },
		),
		Align(
			func(benchWorld) string { return "running" },
			func(w benchWorld) string { return w.unit },
			func(target string, _ benchWorld) string {
				if target == "running" {
					return "START"
				}

				return "STOP"
			},
		),
	}
}

func BenchmarkEngineStepConverged(b *testing.B) {
	engine, err := New(Config[benchWorld, string]{Plan: benchPlan()})
	if err != nil {
		b.Fatal(err)
	}

	world := benchWorld{net: true, unit: "running"}

	for b.Loop() {
		report := engine.Step(world)
		if report.Status != Converged {
			b.Fatal("not converged")
		}
	}
}

func BenchmarkEngineStepOpenFrontier(b *testing.B) {
	engine, err := New(Config[benchWorld, string]{Plan: benchPlan()})
	if err != nil {
		b.Fatal(err)
	}

	world := benchWorld{} // both stages open

	for b.Loop() {
		report := engine.Step(world)
		if report.Status != Frontier {
			b.Fatal("not on the frontier")
		}
	}
}

func BenchmarkChainStep(b *testing.B) {
	chain := Chain(
		Resource[benchWorld, string]{
			Exists: func(w benchWorld) bool { return w.net },
			Invariant: Align(
				func(w benchWorld) bool { return true },
				func(w benchWorld) bool { return w.net },
				func(bool, benchWorld) string { return "NETNS" },
			),
		},
		Resource[benchWorld, string]{
			Exists: func(w benchWorld) bool { return w.unit == "running" },
			Invariant: Align(
				func(benchWorld) string { return "running" },
				func(w benchWorld) string { return w.unit },
				func(target string, _ benchWorld) string {
					if target == "running" {
						return "START"
					}

					return "STOP"
				},
			),
		},
	)

	world := benchWorld{net: true, unit: "running"}

	for b.Loop() {
		done, _ := chain(world)
		if !done {
			b.Fatal("not at rest")
		}
	}
}
