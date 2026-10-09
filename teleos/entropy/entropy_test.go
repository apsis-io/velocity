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
