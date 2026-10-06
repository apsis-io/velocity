// Copyright (C) 2025-2026 Malformed C. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package teleos_test

import "github.com/apsis-io/velocity/teleos"

// World is the observed reality: what is wanted on one side, what the host
// actually has on the other.
type World struct {
	WantNet  bool
	WantUnit string // "running" | "stopped"

	HasNet    bool
	UnitState string // "running" | "stopped"
}

// PodInvariants is the declarative convergence plan: net first, unit second —
// slice order is dependency order, so creation, verification, and repair are
// one list read the same way every pass. Teardown is not: it is the
// topological inverse of bringup, which is what Chain derives on its own —
// no phase flag. The engine consumes PodStages (it reports which one is
// open); PodPlan composes the same list for plain evaluation without an
// engine.
var PodInvariants = []teleos.Invariant[World, string]{
	teleos.Align(
		func(w World) bool { return w.WantNet },
		func(w World) bool { return w.HasNet },
		func(target bool, _ World) string {
			if target {
				return "NETNS_CREATE"
			}

			return "NETNS_DELETE"
		},
	),
	teleos.Align(
		func(w World) string { return w.WantUnit },
		func(w World) string { return w.UnitState },
		func(target string, _ World) string {
			if target == "running" {
				return "UNIT_START"
			}

			return "UNIT_STOP"
		},
	),
}

var PodPlan = teleos.Plan(PodInvariants...)

// PodStageNames is the ordered names PodStages carries, for the satisfied-
// prefix rendering a status endpoint does with Report.Frontier.
var PodStageNames = []string{"NetworkReady", "ContainersReady"}

// PodStages is the same plan as named stages — the shape an Engine's plan
// takes, so the report's Stage field says which stage holds the frontier.
var PodStages = []teleos.Stage[World, string]{
	{Name: "NetworkReady", Check: PodInvariants[0]},
	{Name: "ContainersReady", Check: PodInvariants[1]},
}
