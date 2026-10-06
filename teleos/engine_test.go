// Copyright (C) 2025-2026 Malformed C. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package teleos

import (
	"slices"
	"testing"
)

type podWorld struct {
	wantNet, hasNet     bool
	wantUnit, unitState string
}

// podPlan is the design doc's reconciler: two Align invariants, net first,
// unit second.
func podPlan() []Invariant[podWorld, string] {
	return []Invariant[podWorld, string]{
		Align(
			func(w podWorld) bool { return w.wantNet },
			func(w podWorld) bool { return w.hasNet },
			func(target bool, _ podWorld) string {
				if target {
					return "NETNS_CREATE"
				}

				return "NETNS_DELETE"
			},
		),
		Align(
			func(w podWorld) string { return w.wantUnit },
			func(w podWorld) string { return w.unitState },
			func(target string, _ podWorld) string {
				if target == "running" {
					return "UNIT_START"
				}

				return "UNIT_STOP"
			},
		),
	}
}

// applyString is the test executor for string effects over a podWorld.
func applyString(w *podWorld, eff string) {
	switch eff {
	case "NETNS_CREATE":
		w.hasNet = true
	case "NETNS_DELETE":
		w.hasNet = false
	case "UNIT_START":
		w.unitState = "running"
	case "UNIT_STOP":
		w.unitState = "stopped"
	}
}

func TestEngineConvergesThePodScenarioInThreePasses(t *testing.T) {
	engine, err := New(Config[podWorld, string]{Plan: anonStage(podPlan())})
	if err != nil {
		t.Fatal(err)
	}

	world := podWorld{wantNet: true, wantUnit: "running"}

	var applied []string

	for {
		report := engine.Step(world)
		if report.Status == Converged {
			if report.Frontier != -1 {
				t.Fatalf("converged report names frontier %d, want -1", report.Frontier)
			}

			break
		}

		if report.Status != Frontier {
			t.Fatalf("status = %v mid-convergence, want frontier", report.Status)
		}

		if len(report.Want) != 1 {
			t.Fatalf("want = %v, want one effect per pass", report.Want)
		}

		applied = append(applied, report.Want[0])
		applyString(&world, report.Want[0])
	}

	// The doc's transcript: net first, unit second, converged on the third
	// pass — slice order is dependency order, and convergence is sequential.
	if want := []string{"NETNS_CREATE", "UNIT_START"}; !slices.Equal(applied, want) {
		t.Fatalf("applied = %v, want %v", applied, want)
	}
}

func TestEngineNamesTheOpenFrontier(t *testing.T) {
	engine, err := New(Config[podWorld, string]{Plan: anonStage(podPlan())})
	if err != nil {
		t.Fatal(err)
	}

	// Net satisfied, unit wanted: the frontier is the second invariant.
	report := engine.Step(podWorld{wantNet: true, hasNet: true, wantUnit: "running"})
	if report.Status != Frontier || report.Frontier != 1 {
		t.Fatalf("report = (%v, frontier %d), want frontier 1", report.Status, report.Frontier)
	}

	if len(report.Want) != 1 || report.Want[0] != "UNIT_START" {
		t.Fatalf("want = %v, want UNIT_START", report.Want)
	}
}

// podStages is podPlan as named stages, in the given order of names — the
// shape an Engine's plan takes.
func podStages(names []string) []Stage[podWorld, string] {
	invs := podPlan()

	out := make([]Stage[podWorld, string], len(invs))

	for i, inv := range invs {
		out[i] = Stage[podWorld, string]{Name: names[i], Check: inv}
	}

	return out
}

func TestEngineExhaustsOnAWedge(t *testing.T) {
	wedge := Rule(
		func(testWorld) bool { return false },
		func(testWorld) []string { return make([]string, 200) },
	)

	engine, err := New(Config[testWorld, string]{Plan: anonStage([]Invariant[testWorld, string]{wedge})})
	if err != nil {
		t.Fatal(err)
	}

	report := engine.Step(testWorld{})
	if report.Status != Exhausted {
		t.Fatalf("status = %v, want exhausted for a 200-effect want", report.Status)
	}

	// The wedge's frontier is named, but its want is never handed out: a
	// plan that never stops emitting does not get an executor.
	if report.Frontier != 0 || report.Want != nil {
		t.Fatalf("report = (frontier %d, want %v), want the frontier named and the want withheld", report.Frontier, report.Want)
	}
}

func TestEngineReEmittingTheSameEffectIsNotOscillation(t *testing.T) {
	// A unit that is activating keeps its invariant open across passes and
	// re-emits the same effect. That is convergence working, and the engine
	// must hold the frontier for it, not diagnose a fight.
	engine, err := New(Config[testWorld, string]{
		Plan: anonStage([]Invariant[testWorld, string]{
			Rule(
				func(w testWorld) bool { return w.unit == "running" },
				func(testWorld) []string { return []string{"UNIT_START"} },
			),
		}),
		EffectKey: func(s string) string { return s },
	})
	if err != nil {
		t.Fatal(err)
	}

	frozen := testWorld{unit: "activating"}

	for range 8 {
		report := engine.Step(frozen)
		if report.Status != Frontier {
			t.Fatalf("status = %v on pass %d, want the frontier held", report.Status, report.Passes)
		}

		if len(report.Want) != 1 || report.Want[0] != "UNIT_START" {
			t.Fatalf("want = %v, want UNIT_START re-emitted", report.Want)
		}
	}
}

func TestEngineDetectsPingPongAsOscillating(t *testing.T) {
	// Two invariants that fight: the first wants the world at "a", the
	// second at "b". Applying either re-opens the other.
	fight := []Stage[string, string]{
		{Name: "Lower", Check: Rule(
			func(s string) bool { return s == "a" },
			func(string) []string { return []string{"SET_A"} },
		)},
		{Name: "Upper", Check: Rule(
			func(s string) bool { return s == "b" },
			func(string) []string { return []string{"SET_B"} },
		)},
	}

	apply := func(w *string, eff string) {
		switch eff {
		case "SET_A":
			*w = "a"
		case "SET_B":
			*w = "b"
		}
	}

	engine, err := New(Config[string, string]{
		Plan:      fight,
		EffectKey: func(s string) string { return s },
	})
	if err != nil {
		t.Fatal(err)
	}

	world := "a"

	for {
		report := engine.Step(world)
		if report.Status == Oscillating {
			if report.Want != nil {
				t.Fatalf("oscillating report carried want %v, want it withheld", report.Want)
			}

			return
		}

		if report.Status != Frontier {
			t.Fatalf("status = %v mid-fight, want frontier or oscillating", report.Status)
		}

		apply(&world, report.Want[0])
	}
}

func TestEngineStallsWhenNothingMoves(t *testing.T) {
	engine, err := New(Config[testWorld, string]{
		Plan: anonStage([]Invariant[testWorld, string]{
			Rule(
				func(w testWorld) bool { return w.unit == "running" },
				func(testWorld) []string { return []string{"UNIT_START"} },
			),
		}),
		Same: func(a, b testWorld) bool { return a == b },
	})
	if err != nil {
		t.Fatal(err)
	}

	frozen := testWorld{unit: "activating"}

	for range 2 {
		if report := engine.Step(frozen); report.Status != Frontier {
			t.Fatalf("status = %v on pass %d, want frontier", report.Status, report.Passes)
		}
	}

	report := engine.Step(frozen)
	if report.Status != Stalled {
		t.Fatalf("status = %v after %d unmoved passes, want stalled", report.Status, 3)
	}

	if report.Want != nil {
		t.Fatalf("stalled report carried want %v, want it withheld", report.Want)
	}
}

func TestEngineExhaustsAtThePassBudget(t *testing.T) {
	engine, err := New(Config[testWorld, string]{
		Plan: anonStage([]Invariant[testWorld, string]{
			Rule(
				func(testWorld) bool { return false },
				func(testWorld) []string { return []string{"TRY"} },
			),
		}),
		MaxPasses: 3,
	})
	if err != nil {
		t.Fatal(err)
	}

	for range 3 {
		if report := engine.Step(testWorld{}); report.Status != Frontier {
			t.Fatalf("status = %v within the budget, want frontier", report.Status)
		}
	}

	report := engine.Step(testWorld{})
	if report.Status != Exhausted || report.Passes != 3 {
		t.Fatalf("report = (%v, %d passes), want exhausted at three consumed passes", report.Status, report.Passes)
	}
}

func TestEngineTerminalReportIsIdempotentAndResetResumes(t *testing.T) {
	engine, err := New(Config[testWorld, string]{
		Plan: anonStage([]Invariant[testWorld, string]{
			Rule(
				func(testWorld) bool { return false },
				func(testWorld) []string { return make([]string, 100) },
			),
		}),
		MaxEffects: 10,
	})
	if err != nil {
		t.Fatal(err)
	}

	first := engine.Step(testWorld{})
	if first.Status != Exhausted {
		t.Fatalf("status = %v, want exhausted", first.Status)
	}

	again := engine.Step(testWorld{})
	if again.Status != first.Status || again.Frontier != first.Frontier ||
		again.Passes != first.Passes || again.Effects != first.Effects || again.Want != nil {
		t.Fatalf("terminal report changed: %+v then %+v", first, again)
	}

	engine.Reset()

	resumed := engine.Step(testWorld{})
	if resumed.Status != Exhausted || resumed.Passes != 1 {
		t.Fatalf("reset report = (%v, %d passes), want a fresh engine re-reaching its diagnosis at pass 1", resumed.Status, resumed.Passes)
	}
}

func TestNewRejectsAnEmptyPlan(t *testing.T) {
	if _, err := New(Config[testWorld, string]{}); err == nil {
		t.Fatal("New accepted an empty plan")
	}
}

func TestEngineNamesStagesForIntrospection(t *testing.T) {
	names := []string{"NetworkReady", "ContainersReady"}

	engine, err := New(Config[podWorld, string]{Plan: podStages(names)})
	if err != nil {
		t.Fatal(err)
	}

	world := podWorld{wantNet: true, wantUnit: "running"}

	report := engine.Step(world)
	if report.Stage != "NetworkReady" || report.Frontier != 0 {
		t.Fatalf("report = (stage %q, frontier %d), want NetworkReady at 0", report.Stage, report.Frontier)
	}

	world.hasNet = true

	report = engine.Step(world)
	if report.Stage != "ContainersReady" || report.Frontier != 1 {
		t.Fatalf("report = (stage %q, frontier %d), want ContainersReady at 1", report.Stage, report.Frontier)
	}

	world.unitState = "running"

	report = engine.Step(world)
	if report.Status != Converged || report.Stage != "" || report.Frontier != -1 {
		t.Fatalf("report = (%v, stage %q, frontier %d), want convergence with no stage", report.Status, report.Stage, report.Frontier)
	}

	// Mid-progress, the satisfied prefix is the readiness conditions a
	// status endpoint renders: everything before the frontier, in order.
	if got := names[:1]; !slices.Equal(got, []string{"NetworkReady"}) {
		t.Fatalf("completed = %v, want the satisfied prefix", got)
	}
}

func TestNewRejectsNilStageChecks(t *testing.T) {
	if _, err := New(Config[podWorld, string]{Plan: []Stage[podWorld, string]{{Name: "x"}}}); err == nil {
		t.Fatal("New accepted a stage with a nil Check")
	}
}

func TestNewRejectsNilPlanEntries(t *testing.T) {
	_, err := New(Config[testWorld, string]{
		Plan: anonStage([]Invariant[testWorld, string]{
			Rule(func(testWorld) bool { return true }, func(testWorld) []string { return nil }),
			nil,
		}),
	})
	if err == nil {
		t.Fatal("New accepted a nil invariant — it would panic at the first Step")
	}
}

// TestEngineNilEffectKeyDisablesOscillationDetection pins the documented
// boundary: without an EffectKey the engine cannot tell one effect from
// another, so it never cries oscillation — even on a genuine fight. The
// budget is what stops it.
func TestEngineNilEffectKeyDisablesOscillationDetection(t *testing.T) {
	engine, err := New(Config[string, string]{
		Plan:      anonStage(fightPlan()),
		MaxPasses: 6,
	})
	if err != nil {
		t.Fatal(err)
	}

	world := "a"

	for range 6 {
		report := engine.Step(world)
		if report.Status == Oscillating {
			t.Fatal("oscillation diagnosed without an EffectKey to detect it with")
		}

		if report.Status == Frontier {
			if report.Want[0] == "SET_A" {
				world = "a"
			} else {
				world = "b"
			}
		}
	}
}

// TestEngineStallBeatsOscillationOnAFrozenWorld: a world that does not move
// with the same effect re-emitted is a stall, not a fight — the sharper
// diagnosis wins only when the signatures actually alternate.
func TestEngineStallBeatsOscillationOnAFrozenWorld(t *testing.T) {
	engine, err := New(Config[testWorld, string]{
		Plan: anonStage([]Invariant[testWorld, string]{
			Rule(
				func(w testWorld) bool { return w.unit == "running" },
				func(testWorld) []string { return []string{"UNIT_START"} },
			),
		}),
		EffectKey: func(s string) string { return s },
		Same:      func(a, b testWorld) bool { return a == b },
	})
	if err != nil {
		t.Fatal(err)
	}

	frozen := testWorld{unit: "activating"}

	var last Report[testWorld, string]

	for range 3 {
		last = engine.Step(frozen)
	}

	if last.Status != Stalled {
		t.Fatalf("status = %v, want stalled — the world never moved", last.Status)
	}
}

// TestEngineLongerCyclesAreNotOscillation pins the documented bound: the
// detector is a two-slot ring, so A-B-B-A slips past it and the pass budget
// is what ends the run. If the ring ever grows, this test is the one to
// turn into a positive.
func TestEngineLongerCyclesAreNotOscillation(t *testing.T) {
	// A three-state cycle a → b → c → a: no signature is two passes back
	// until it repeats exactly, which it never does.
	engine, err := New(Config[string, string]{
		Plan: anonStage([]Invariant[string, string]{
			Rule(
				func(s string) bool { return s == "a" },
				func(string) []string { return []string{"TO_B"} },
			),
			Rule(
				func(s string) bool { return s == "c" },
				func(string) []string { return []string{"TO_A"} },
			),
		}),
		EffectKey: func(s string) string { return s },
		MaxPasses: 9,
	})
	if err != nil {
		t.Fatal(err)
	}

	world := "a"
	sawOscillating := false

	for range 9 {
		report := engine.Step(world)
		if report.Status == Oscillating {
			sawOscillating = true

			break
		}

		if report.Status == Frontier {
			switch report.Want[0] {
			case "TO_B":
				world = "b"
			case "TO_A":
				world = "a"
			}
		}
	}

	if sawOscillating {
		t.Fatal("a-b-c-a diagnosed as a two-pass ping-pong — the detector grew without this test being updated")
	}
}

func TestEngineWedgeBoundaryIsExactlyAtTheCap(t *testing.T) {
	for _, cap_ := range []int{1, 4, 64} {
		exact := Rule(
			func(testWorld) bool { return false },
			func(testWorld) []string { return make([]string, cap_) },
		)

		engine, err := New(Config[testWorld, string]{Plan: anonStage([]Invariant[testWorld, string]{exact}), MaxEffects: cap_})
		if err != nil {
			t.Fatal(err)
		}

		if report := engine.Step(testWorld{}); report.Status != Frontier {
			t.Fatalf("cap %d: status = %v for a want exactly at the cap, want frontier", cap_, report.Status)
		}

		over := Rule(
			func(testWorld) bool { return false },
			func(testWorld) []string { return make([]string, cap_+1) },
		)

		engine, err = New(Config[testWorld, string]{Plan: anonStage([]Invariant[testWorld, string]{over}), MaxEffects: cap_})
		if err != nil {
			t.Fatal(err)
		}

		if report := engine.Step(testWorld{}); report.Status != Exhausted {
			t.Fatalf("cap %d: status = %v for a want one over, want exhausted", cap_, report.Status)
		}
	}
}

// TestEngineWedgeNamesItsStage: a diagnosis that names a frontier names the
// stage too — an Exhausted report with an unnamed stage sends the reader
// counting indices.
func TestEngineWedgeNamesItsStage(t *testing.T) {
	engine, err := New(Config[testWorld, string]{
		Plan: []Stage[testWorld, string]{
			{Name: "NetworkReady", Check: Rule(func(w testWorld) bool { return w.net }, func(testWorld) []string { return nil })},
			{Name: "UnitReady", Check: Rule(
				func(testWorld) bool { return false },
				func(testWorld) []string { return make([]string, 99) },
			)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	report := engine.Step(testWorld{net: true})
	if report.Status != Exhausted || report.Stage != "UnitReady" {
		t.Fatalf("report = (%v, stage %q), want the wedge named at UnitReady", report.Status, report.Stage)
	}
}

func TestEngineStallReportNamesItsStage(t *testing.T) {
	engine, err := New(Config[testWorld, string]{
		Plan: []Stage[testWorld, string]{
			{Name: "UnitReady", Check: Rule(
				func(w testWorld) bool { return w.unit == "running" },
				func(testWorld) []string { return []string{"UNIT_START"} },
			)},
		},
		Same: func(a, b testWorld) bool { return a == b },
	})
	if err != nil {
		t.Fatal(err)
	}

	frozen := testWorld{unit: "activating"}

	for range 3 {
		last := engine.Step(frozen)
		if last.Status == Stalled && last.Stage != "UnitReady" {
			t.Fatalf("stall stage = %q, want UnitReady", last.Stage)
		}
	}
}

func TestEngineResetFromStalledResumes(t *testing.T) {
	engine, err := New(Config[testWorld, string]{
		Plan: anonStage([]Invariant[testWorld, string]{
			Rule(
				func(w testWorld) bool { return w.unit == "running" },
				func(testWorld) []string { return []string{"UNIT_START"} },
			),
		}),
		EffectKey: func(s string) string { return s },
		Same:      func(a, b testWorld) bool { return a == b },
	})
	if err != nil {
		t.Fatal(err)
	}

	frozen := testWorld{unit: "activating"}

	for range 3 {
		engine.Step(frozen)
	}

	engine.Reset()

	world := testWorld{unit: "running"}

	report := engine.Step(world)
	if report.Status != Converged {
		t.Fatalf("status = %v after Reset onto a satisfied world, want converged", report.Status)
	}
}

func TestEngineEffectsCounterAccumulates(t *testing.T) {
	engine, err := New(Config[podWorld, string]{Plan: anonStage(podPlan())})
	if err != nil {
		t.Fatal(err)
	}

	world := podWorld{wantNet: true, wantUnit: "running"}

	var last Report[podWorld, string]

	for {
		last = engine.Step(world)
		if last.Status != Frontier {
			break
		}

		if last.Effects != last.Passes {
			t.Fatalf("effects = %d at pass %d with one effect per pass", last.Effects, last.Passes)
		}

		applyString(&world, last.Want[0])
	}
}

// anonStage wraps bare invariants as anonymous stages, for tests of engine
// mechanics that do not care about names.
func anonStage[T any, E any](invs []Invariant[T, E]) []Stage[T, E] {
	out := make([]Stage[T, E], len(invs))

	for i, inv := range invs {
		out[i] = Stage[T, E]{Check: inv}
	}

	return out
}

// fightPlan is the two-invariant ping-pong: a wants the world at "a", the
// other at "b".
func fightPlan() []Invariant[string, string] {
	return []Invariant[string, string]{
		Rule(
			func(s string) bool { return s == "a" },
			func(string) []string { return []string{"SET_A"} },
		),
		Rule(
			func(s string) bool { return s == "b" },
			func(string) []string { return []string{"SET_B"} },
		),
	}
}

func TestEngineResetFromOscillatingResumes(t *testing.T) {
	engine, err := New(Config[string, string]{
		Plan:      anonStage(fightPlan()),
		EffectKey: func(s string) string { return s },
	})
	if err != nil {
		t.Fatal(err)
	}

	world := "a"

	for {
		report := engine.Step(world)
		if report.Status == Oscillating {
			break
		}

		if report.Status == Frontier {
			if report.Want[0] == "SET_A" {
				world = "a"
			} else {
				world = "b"
			}
		}
	}

	engine.Reset()

	// The fight is still there — Reset clears a diagnosis, not a conflict —
	// so the engine re-reaches it from pass one.
	report := engine.Step("a")
	if report.Status != Frontier || report.Passes != 1 {
		t.Fatalf("report = (%v, pass %d), want a fresh frontier at pass 1", report.Status, report.Passes)
	}
}

func TestEngineOscillatingReportNamesItsStage(t *testing.T) {
	engine, err := New(Config[string, string]{
		Plan: []Stage[string, string]{
			{Name: "LowerHalf", Check: Rule(
				func(s string) bool { return s == "a" },
				func(string) []string { return []string{"SET_A"} },
			)},
			{Name: "UpperHalf", Check: Rule(
				func(s string) bool { return s == "b" },
				func(string) []string { return []string{"SET_B"} },
			)},
		},
		EffectKey: func(s string) string { return s },
	})
	if err != nil {
		t.Fatal(err)
	}

	world := "a"

	for {
		report := engine.Step(world)
		if report.Status == Oscillating {
			if report.Stage != "UpperHalf" {
				t.Fatalf("oscillation stage = %q, want UpperHalf", report.Stage)
			}

			return
		}

		if report.Status == Frontier {
			if report.Want[0] == "SET_A" {
				world = "a"
			} else {
				world = "b"
			}
		}
	}
}

func TestEngineNegativeConfigFallsBackToDefaults(t *testing.T) {
	// A wedge of 200 effects trips the default 64 cap under negative
	// configuration, proving the fallback rather than the literal.
	engine, err := New(Config[testWorld, string]{
		Plan: anonStage([]Invariant[testWorld, string]{
			Rule(
				func(testWorld) bool { return false },
				func(testWorld) []string { return make([]string, 200) },
			),
		}),
		MaxEffects: -5,
		MaxPasses:  -5,
	})
	if err != nil {
		t.Fatal(err)
	}

	if report := engine.Step(testWorld{}); report.Status != Exhausted {
		t.Fatalf("status = %v, want the default cap to catch a 200-effect want", report.Status)
	}
}
