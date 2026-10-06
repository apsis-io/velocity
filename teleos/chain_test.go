// Copyright (C) 2025-2026 Malformed C. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package teleos

import (
	"math/rand"
	"slices"
	"testing"
)

// chainWorld is a three-link foundation: a netns under mounts under a
// process.
type chainWorld struct {
	hasNet, wantNet       bool
	hasMounts, wantMounts bool
	proc, wantProc        string // "running" | "stopped"

	// netDriftResolved stands in for an observed configuration change that
	// has not been applied yet: the link exists but is not at rest.
	netDriftResolved bool
}

// chainHarness builds the netns → mounts → process chain and the effect
// applier the audit's traces run against.
func chainHarness() ([]Resource[chainWorld, string], func(*chainWorld, string)) {
	resources := []Resource[chainWorld, string]{
		{
			Exists: func(w chainWorld) bool { return w.hasNet },
			Invariant: Align(
				func(w chainWorld) bool { return w.wantNet },
				func(w chainWorld) bool { return w.hasNet },
				func(target bool, _ chainWorld) string {
					if target {
						return "CREATE_NETNS"
					}

					return "DELETE_NETNS"
				},
			),
		},
		{
			Exists: func(w chainWorld) bool { return w.hasMounts },
			Invariant: Align(
				func(w chainWorld) bool { return w.wantMounts },
				func(w chainWorld) bool { return w.hasMounts },
				func(target bool, _ chainWorld) string {
					if target {
						return "MOUNT"
					}

					return "UNMOUNT"
				},
			),
		},
		{
			// The process's desired state is existence-style: "running" or
			// not, never the empty string — a resource's not-existing is a
			// state reality can actually reach.
			Exists: func(w chainWorld) bool { return w.proc == "running" },
			Invariant: Align(
				func(w chainWorld) bool { return w.wantProc == "running" },
				func(w chainWorld) bool { return w.proc == "running" },
				func(target bool, _ chainWorld) string {
					if target {
						return "START_PROCESS"
					}

					return "SIGTERM_PROCESS"
				},
			),
		},
	}

	apply := func(w *chainWorld, eff string) {
		switch eff {
		case "CREATE_NETNS":
			w.hasNet = true
		case "DELETE_NETNS":
			w.hasNet = false
		case "MOUNT":
			w.hasMounts = true
		case "UNMOUNT":
			w.hasMounts = false
		case "START_PROCESS":
			w.proc = "running"
		case "SIGTERM_PROCESS":
			w.proc = "stopped"
		}
	}

	return resources, apply
}

// runChain drives a chain to rest, one effect per pass, and returns where
// the world ended up and what was applied to get there.
func runChain(t *testing.T, chain Invariant[chainWorld, string], apply func(*chainWorld, string), world chainWorld) (chainWorld, []string) {
	t.Helper()

	var applied []string

	for range 16 {
		done, want := chain(world)
		if done {
			return world, applied
		}

		if len(want) != 1 {
			t.Fatalf("want = %v, want one effect per pass", want)
		}

		applied = append(applied, want[0])
		apply(&world, want[0])
	}

	t.Fatalf("the chain never converged in 16 passes: applied=%v world=%+v", applied, world)

	return world, applied
}

// TestChainDeletionStopsTheProcessFirst is the deletion trace: everything
// wanted gone, everything observed present. The process stops, then the
// mounts unmount, then the netns deletes — no phase flag anywhere in the
// plan, because the release sweep runs leaf to root.
func TestChainDeletionStopsTheProcessFirst(t *testing.T) {
	resources, apply := chainHarness()

	world, applied := runChain(t, Chain(resources...), apply, chainWorld{hasNet: true, hasMounts: true, proc: "running"})

	if want := []string{"SIGTERM_PROCESS", "UNMOUNT", "DELETE_NETNS"}; !slices.Equal(applied, want) {
		t.Fatalf("applied = %v, want teardown as the leaf-to-root release", applied)
	}

	if world.hasNet || world.hasMounts || world.proc != "stopped" {
		t.Fatalf("world = %+v, want everything released", world)
	}
}

// TestChainBringupHoldsUntilTheFoundationDoes is the acquire trace: the
// process starts only after the netns and mounts hold.
func TestChainBringupHoldsUntilTheFoundationDoes(t *testing.T) {
	resources, apply := chainHarness()

	world, applied := runChain(t, Chain(resources...), apply, chainWorld{wantNet: true, wantMounts: true, wantProc: "running"})

	if want := []string{"CREATE_NETNS", "MOUNT", "START_PROCESS"}; !slices.Equal(applied, want) {
		t.Fatalf("applied = %v, want bringup as the root-to-leaf acquire", applied)
	}

	if !world.hasNet || !world.hasMounts || world.proc != "running" {
		t.Fatalf("world = %+v, want everything acquired", world)
	}
}

// TestChainCrashedProcessRestartsWithoutTouchingTheFoundation is the acid
// trace: the process died, the foundation holds, and the chain must start
// the process without deleting or rebuilding anything beneath it. The
// release sweep finds nothing pending; the acquire sweep finds one gap.
func TestChainCrashedProcessRestartsWithoutTouchingTheFoundation(t *testing.T) {
	resources, apply := chainHarness()

	started := chainWorld{wantNet: true, hasNet: true, wantMounts: true, hasMounts: true, wantProc: "running", proc: "stopped"}

	world, applied := runChain(t, Chain(resources...), apply, started)

	if want := []string{"START_PROCESS"}; !slices.Equal(applied, want) {
		t.Fatalf("applied = %v, want only the process started", applied)
	}

	if world.proc != "running" || !world.hasNet || !world.hasMounts {
		t.Fatalf("world = %+v, want the process started on an untouched foundation", world)
	}
}

// TestChainDriftingFoundationPreemptsDownstreamBuilds pins the conservative
// reading of a link that exists but is not at rest: the process's start
// waits until the drifting netns settles, because the release sweep halts
// on an existing, open link before the acquire sweep runs at all.
func TestChainDriftingFoundationPreemptsDownstreamBuilds(t *testing.T) {
	resources, apply := chainHarness()

	// The netns exists but is drifting: a second subnet was requested and
	// has not been applied. Its Rule opens on the drift until it resolves.
	drifted := []Resource[chainWorld, string]{
		{
			Exists: func(w chainWorld) bool { return w.hasNet },
			Invariant: Rule(
				func(w chainWorld) bool { return w.netDriftResolved },
				func(chainWorld) []string { return []string{"ATTACH_SUBNET"} },
			),
		},
		resources[2],
	}

	world := chainWorld{hasNet: true, wantNet: true, wantProc: "running"}

	done, want := Chain(drifted...)(world)
	if done {
		t.Fatal("chain reported done with the foundation drifting")
	}

	if len(want) != 1 || want[0] != "ATTACH_SUBNET" {
		t.Fatalf("want = %v, want the drift's effect before any downstream acquire", want)
	}

	world.netDriftResolved = true

	world, applied := runChain(t, Chain(drifted...), apply, world)

	// The drift's own effect was asserted above; from the resolved world
	// only the process start remains.
	if want := []string{"START_PROCESS"}; !slices.Equal(applied, want) {
		t.Fatalf("applied = %v, want only the process started once the drift resolved", applied)
	}
}

// TestForwardPlanDeletesTheNetnsUnderARunningUnit keeps the trap that Chain
// exists to close: a forward sweep over bringup invariants, handed a
// terminating world, halts at the first gap it meets and deletes the netns
// while the process still runs in it.
func TestForwardPlanDeletesTheNetnsUnderARunningUnit(t *testing.T) {
	world := podWorld{wantNet: false, hasNet: true, wantUnit: "stopped", unitState: "running"}

	done, want := Plan(podPlan()...)(world)
	if done {
		t.Fatal("forward plan reported done mid-teardown")
	}

	if len(want) != 1 || want[0] != "NETNS_DELETE" {
		t.Fatalf("want = %v, want the trap: NETNS_DELETE first", want)
	}
}

func TestChainWithNoLinksIsDone(t *testing.T) {
	done, want := Chain[chainWorld, string]()(chainWorld{})
	if !done || want != nil {
		t.Fatalf("vacuous chain = (%v, %v), want done", done, want)
	}
}

// TestChainReleasePreemptsAnUnrelatedAcquire pins the rolling-replacement
// shape: one link releasing (the old process stopping) happens before a
// different, independent link acquires (a netns being created) — the
// release sweep runs to the bottom before the acquire sweep starts.
func TestChainReleasePreemptsAnUnrelatedAcquire(t *testing.T) {
	resources, apply := chainHarness()

	// Mounts wanted gone (and present); netns wanted present (and absent).
	// The chain is mounts under netns is not the physical story here — what
	// matters is that a pending release anywhere preempts every acquire.
	mixed := []Resource[chainWorld, string]{
		{
			Exists: func(w chainWorld) bool { return w.hasNet },
			Invariant: Align(
				func(w chainWorld) bool { return w.wantNet },
				func(w chainWorld) bool { return w.hasNet },
				func(target bool, _ chainWorld) string {
					if target {
						return "CREATE_NETNS"
					}

					return "DELETE_NETNS"
				},
			),
		},
		resources[1], // the mounts link, wanted gone
	}

	world := chainWorld{wantNet: true, hasMounts: true, wantMounts: false}

	done, want := Chain(mixed...)(world)
	if done {
		t.Fatal("mixed chain reported done with both gaps open")
	}

	if len(want) != 1 || want[0] != "UNMOUNT" {
		t.Fatalf("want = %v, want the release to preempt the acquire", want)
	}

	apply(&world, want[0])

	done, want = Chain(mixed...)(world)
	if done || len(want) != 1 || want[0] != "CREATE_NETNS" {
		t.Fatalf("after the release: (%v, %v), want the acquire next", done, want)
	}
}

// TestChainPropertyNeverRemovesWhatIsStoodOn is the release sweep's
// guarantee stated as a property over random worlds: the chain never
// removes a link while a link stands above it (present), and never
// acquires a link while the link beneath it is absent. Intents are drawn
// prefix-shaped — the links that should exist form a prefix of the chain —
// because that is what a satisfiable chain intent is; contradictory intent
// is the documented unresolvable class, and no ordering property can hold
// for it. Three hundred random worlds, each driven to rest by a faithful
// executor.
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

		chain := Chain(
			propLink(0, &desired, &observed),
			propLink(1, &desired, &observed),
			propLink(2, &desired, &observed),
		)

		for pass := range 12 {
			done, want := chain(observed)
			if done {
				break
			}

			if len(want) != 1 {
				t.Fatalf("iter %d pass %d: want = %v, want one effect per pass", iter, pass, want)
			}

			eff := want[0]
			removing := eff[0] == 'D'
			idx := int(eff[1] - '0')

			for j := range 3 {
				switch {
				case removing && j > idx && observed[j]:
					t.Fatalf("iter %d pass %d: removing link %d while link %d stands on it", iter, pass, idx, j)
				case !removing && j < idx && !observed[j]:
					t.Fatalf("iter %d pass %d: acquiring link %d while link %d beneath it is absent", iter, pass, idx, j)
				}
			}

			// Faithful executor: the effect lands exactly as emitted.
			observed[idx] = desired[idx]
		}

		if done, _ := chain(observed); !done {
			t.Fatalf("iter %d: chain not at rest after the executor obeyed every effect", iter)
		}
	}
}

// propLink builds the chain link for one index over shared intent and
// observation arrays: existence-style Align, the shape real chains use.
func propLink(i int, desired, observed *[3]bool) Resource[[3]bool, string] {
	return Resource[[3]bool, string]{
		Exists: func(w [3]bool) bool { return observed[i] },
		Invariant: Align(
			func([3]bool) bool { return desired[i] },
			func(w [3]bool) bool { return observed[i] },
			func(target bool, _ [3]bool) string {
				if target {
					return "A" + string(rune('0'+i))
				}

				return "D" + string(rune('0'+i))
			},
		),
	}
}
