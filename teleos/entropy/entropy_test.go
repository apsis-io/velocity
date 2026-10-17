// Copyright (C) 2025-2026 Malformed C. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package entropy_test

import (
	"math/rand"
	"testing"

	"github.com/apsis-io/velocity/teleos"
	"github.com/apsis-io/velocity/teleos/entropy"
)

// A two-link stack: a netns under a process.
type stackWorld struct {
	hasNet, wantNet    bool
	procUp, wantProcUp bool
}

func stackStages() []teleos.Stage[stackWorld, string] {
	return []teleos.Stage[stackWorld, string]{
		{Name: "Netns", Check: teleos.Align(
			func(w stackWorld) bool { return w.wantNet },
			func(w stackWorld) bool { return w.hasNet },
			func(target bool, _ stackWorld) string {
				if target {
					return "CREATE_NETNS"
				}

				return "DELETE_NETNS"
			},
		)},
		{Name: "Process", Check: teleos.Align(
			func(w stackWorld) bool { return w.wantProcUp },
			func(w stackWorld) bool { return w.procUp },
			func(target bool, _ stackWorld) string {
				if target {
					return "START_PROCESS"
				}

				return "STOP_PROCESS"
			},
		)},
	}
}

func stackApply(w stackWorld, eff string) stackWorld {
	switch eff {
	case "CREATE_NETNS":
		w.hasNet = true
	case "DELETE_NETNS":
		w.hasNet = false
	case "START_PROCESS":
		w.procUp = true
	case "STOP_PROCESS":
		w.procUp = false
	}

	return w
}

func TestHarnessFaithfulWorldConverges(t *testing.T) {
	v := entropy.Run(t, entropy.Config[stackWorld, string]{
		Stages: stackStages(),
		Apply:  stackApply,
	}, stackWorld{wantNet: true, wantProcUp: true})

	if !v.Converged {
		t.Fatalf("verdict = %+v, want convergence", v)
	}

	if !v.Final.hasNet || !v.Final.procUp {
		t.Fatalf("final = %+v, want the stack up", v.Final)
	}
}

// TestHarnessDriftRecovery asserts gravity: converge, an administrator
// deletes the netns, the engine is Reset — and the plan pulls the world
// back. A plan that cannot survive its own success is not done.
func TestHarnessDriftRecovery(t *testing.T) {
	v := entropy.Recover(t, entropy.Config[stackWorld, string]{
		Stages: stackStages(),
		Apply:  stackApply,
	}, stackWorld{wantNet: true, wantProcUp: true}, func(w stackWorld) stackWorld {
		w.hasNet = false // the netns is externally deleted

		return w
	})

	if !v.Converged {
		t.Fatalf("recovery verdict = %+v", v)
	}

	if !v.Final.hasNet {
		t.Fatalf("final = %+v, want the netns rebuilt", v.Final)
	}
}

// TestHarnessTotalEntropyReachesADiagnosis: an entropy that erases
// everything the effects build means the world can never converge. The
// engine must say so within its budget — a diagnosis, never a spin. With
// Same set the diagnosis is Stalled (the world is frozen); without it, the
// budget closes the run as Exhausted.
func TestHarnessTotalEntropyReachesADiagnosis(t *testing.T) {
	storm := func(pass int, w stackWorld, _ *rand.Rand) stackWorld {
		w.hasNet = false
		w.procUp = false

		return w
	}

	t.Run("stalled with Same", func(t *testing.T) {
		v := entropy.Run(t, entropy.Config[stackWorld, string]{
			Stages: stackStages(),
			Apply:  stackApply,
			Entropy: func(pass int, w stackWorld, rng *rand.Rand) stackWorld {
				return storm(pass, w, rng)
			},
			Same:      func(a, b stackWorld) bool { return a == b },
			MaxPasses: 24,
		}, stackWorld{wantNet: true, wantProcUp: true})

		if v.Status != teleos.Stalled {
			t.Fatalf("status = %v, want stalled under a frozen storm", v.Status)
		}
	})

	t.Run("exhausted without Same", func(t *testing.T) {
		v := entropy.Run(t, entropy.Config[stackWorld, string]{
			Stages: stackStages(),
			Apply:  stackApply,
			Entropy: func(pass int, w stackWorld, rng *rand.Rand) stackWorld {
				return storm(pass, w, rng)
			},
			MaxPasses: 24,
		}, stackWorld{wantNet: true, wantProcUp: true})

		if v.Status != teleos.Exhausted {
			t.Fatalf("status = %v, want the budget to close a world that cannot converge", v.Status)
		}
	})
}

// TestHarnessDriftRecoveryPreservesConvergedStateAndAudits verifies Bug #3:
// Recover drifts first.Final (not the initial un-converged world), reuses and
// Resets the same engine instance, and runs Audit on recovery passes.
func TestHarnessDriftRecoveryPreservesConvergedStateAndAudits(t *testing.T) {
	var recoveryAudits int

	v := entropy.Recover(t, entropy.Config[stackWorld, string]{
		Stages: stackStages(),
		Apply:  stackApply,
		Audit: func(pass int, w stackWorld, want []string) error {
			if pass > 3 {
				recoveryAudits++
			}

			return nil
		},
	}, stackWorld{wantNet: true, wantProcUp: true}, func(w stackWorld) stackWorld {
		if !w.hasNet || !w.procUp {
			t.Fatalf("drift received un-converged world %+v, want first.Final", w)
		}

		w.procUp = false // only the process crashes; netns stays up

		return w
	})

	if !v.Converged || !v.Final.hasNet || !v.Final.procUp {
		t.Fatalf("recovery verdict = %+v", v)
	}

	if v.Effects != 1 {
		t.Fatalf("recovery effects = %d, want 1 (only START_PROCESS, not full rebuild)", v.Effects)
	}

	if recoveryAudits == 0 {
		t.Fatal("cfg.Audit was not called during recovery passes")
	}
}

// TestHarnessEntropyRunsOncePerPassAndAdvancesHolds verifies Bug #4:
// Entropy is invoked once per pass (not once per effect inside a batch) and
// can advance an in-flight hold (Frontier with len(Want) == 0).
func TestHarnessEntropyRunsOncePerPassAndAdvancesHolds(t *testing.T) {
	var entropyCalls int

	v := entropy.Run(t, entropy.Config[stackWorld, string]{
		Stages: []teleos.Stage[stackWorld, string]{
			{Name: "Both", Check: teleos.Concurrent(
				stackStages()[0].Check,
				stackStages()[1].Check,
			)},
			{Name: "WaitSettle", Check: teleos.Rule(
				func(w stackWorld) bool { return entropyCalls >= 2 },
				func(stackWorld) []string { return nil }, // hold until Entropy advances
			)},
		},
		Apply: stackApply,
		Entropy: func(_ int, w stackWorld, _ *rand.Rand) stackWorld {
			entropyCalls++

			return w
		},
		MaxPasses: 8,
	}, stackWorld{wantNet: true, wantProcUp: true})

	if !v.Converged {
		t.Fatalf("verdict = %+v, want convergence after hold advanced", v)
	}

	// Pass 1 emits 2 concurrent effects (CREATE_NETNS, START_PROCESS) -> 1 Entropy call.
	// Pass 2 hits the WaitSettle hold (0 effects) -> 2nd Entropy call.
	// Pass 3 observes entropyCalls == 2 -> Converged.
	if entropyCalls != 2 {
		t.Fatalf("entropyCalls = %d, want 2 (once per Frontier pass, not per effect)", entropyCalls)
	}
}
