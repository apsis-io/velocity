// Copyright (C) 2025-2026 Malformed C. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package teleos

import "testing"

type testWorld struct {
	net  bool
	unit string // "running" | "stopped"
}

func TestPlanHaltsAtTheFrontier(t *testing.T) {
	var evals int

	plan := Plan[testWorld, string](
		Rule(
			func(w testWorld) bool { return w.net },
			func(testWorld) []string { return []string{"NETNS_CREATE"} },
		),
		Rule(
			func(w testWorld) bool {
				evals++

				return w.unit == "running"
			},
			func(testWorld) []string { return []string{"UNIT_START"} },
		),
	)

	done, want := plan(testWorld{})
	if done {
		t.Fatal("plan reported done on an empty world")
	}

	if len(want) != 1 || want[0] != "NETNS_CREATE" {
		t.Fatalf("want = %v, want the first invariant's effect", want)
	}

	// The frontier is the whole point: while the first invariant stands open,
	// the second is never asked.
	if evals != 0 {
		t.Fatalf("second invariant's sensor ran %d times at an open frontier, want 0", evals)
	}

	done, _ = plan(testWorld{net: true})
	if done {
		t.Fatal("plan reported done with the unit still stopped")
	}

	if evals != 1 {
		t.Fatalf("second invariant's sensor ran %d times once the first held, want 1", evals)
	}
}

func TestPlanConvergesWhenEveryInvariantHolds(t *testing.T) {
	plan := Plan[testWorld, string](
		Rule(
			func(w testWorld) bool { return w.net },
			func(testWorld) []string { return []string{"NETNS_CREATE"} },
		),
		Rule(
			func(w testWorld) bool { return w.unit == "running" },
			func(testWorld) []string { return []string{"UNIT_START"} },
		),
	)

	done, want := plan(testWorld{net: true, unit: "running"})
	if !done {
		t.Fatal("plan reported an open frontier on a satisfied world")
	}

	if want != nil {
		t.Fatalf("want = %v, want nil at convergence", want)
	}
}

func TestRuleEmitsOnlyWhenNotDone(t *testing.T) {
	var wants int

	rule := Rule(
		func(w testWorld) bool { return w.net },
		func(testWorld) []string {
			wants++

			return []string{"NETNS_CREATE"}
		},
	)

	if done, _ := rule(testWorld{net: true}); !done {
		t.Fatal("rule open on a satisfied world")
	}

	if wants != 0 {
		t.Fatalf("want ran %d times on a satisfied world, want 0", wants)
	}

	if done, want := rule(testWorld{}); done || len(want) != 1 {
		t.Fatalf("rule = (%v, %v) on an empty world", done, want)
	}
}

func TestAlignClosesABoolGap(t *testing.T) {
	align := Align(
		func(w testWorld) bool { return true },
		func(w testWorld) bool { return w.net },
		func(target bool, _ testWorld) string {
			if target {
				return "NETNS_CREATE"
			}

			return "NETNS_DELETE"
		},
	)

	done, want := align(testWorld{net: true})
	if !done || want != nil {
		t.Fatalf("align = (%v, %v) on a matching world", done, want)
	}

	done, want = align(testWorld{})
	if done || len(want) != 1 || want[0] != "NETNS_CREATE" {
		t.Fatalf("align = (%v, %v) on a gap, want NETNS_CREATE", done, want)
	}
}

func TestAlignClosesAStringGap(t *testing.T) {
	align := Align(
		func(w testWorld) string { return "running" },
		func(w testWorld) string { return w.unit },
		func(target string, _ testWorld) string {
			if target == "running" {
				return "UNIT_START"
			}

			return "UNIT_STOP"
		},
	)

	done, want := align(testWorld{unit: "stopped"})
	if done || len(want) != 1 || want[0] != "UNIT_START" {
		t.Fatalf("align = (%v, %v) on a stopped unit, want UNIT_START", done, want)
	}

	done, _ = align(testWorld{unit: "running"})
	if !done {
		t.Fatal("align open on a running unit")
	}
}
