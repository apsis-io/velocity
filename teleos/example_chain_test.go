// Copyright (C) 2025-2026 Malformed C. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package teleos_test

import (
	"fmt"

	"github.com/apsis-io/velocity/teleos"
)

// DocStack is a three-link foundation for the Chain examples: a netns under
// mounts under a process.
type DocStack struct {
	hasNet, wantNet       bool
	hasMounts, wantMounts bool
	proc, wantProc        string // "running" | "stopped"
}

// docStackChain builds the chain once; the examples below drive it through
// different intents and watch the direction come out right in both.
func docStackChain() (teleos.Invariant[DocStack, string], func(*DocStack, string)) {
	stack := []teleos.Resource[DocStack, string]{
		{
			Exists: func(s DocStack) bool { return s.hasNet },
			Invariant: teleos.Align(
				func(s DocStack) bool { return s.wantNet },
				func(s DocStack) bool { return s.hasNet },
				func(target bool, _ DocStack) string {
					if target {
						return "CREATE_NETNS"
					}

					return "DELETE_NETNS"
				},
			),
		},
		{
			Exists: func(s DocStack) bool { return s.hasMounts },
			Invariant: teleos.Align(
				func(s DocStack) bool { return s.wantMounts },
				func(s DocStack) bool { return s.hasMounts },
				func(target bool, _ DocStack) string {
					if target {
						return "MOUNT_VOLUMES"
					}

					return "UNMOUNT_VOLUMES"
				},
			),
		},
		{
			Exists: func(s DocStack) bool { return s.proc == "running" },
			Invariant: teleos.Align(
				func(s DocStack) bool { return s.wantProc == "running" },
				func(s DocStack) bool { return s.proc == "running" },
				func(target bool, _ DocStack) string {
					if target {
						return "START_PROCESS"
					}

					return "SIGTERM_PROCESS"
				},
			),
		},
	}

	apply := func(s *DocStack, eff string) {
		switch eff {
		case "CREATE_NETNS":
			s.hasNet = true
		case "DELETE_NETNS":
			s.hasNet = false
		case "MOUNT_VOLUMES":
			s.hasMounts = true
		case "UNMOUNT_VOLUMES":
			s.hasMounts = false
		case "START_PROCESS":
			s.proc = "running"
		case "SIGTERM_PROCESS":
			s.proc = "stopped"
		}
	}

	return teleos.Chain(stack...), apply
}

// Deleting the stack: everything wanted gone, everything observed present.
// There is no phase flag and no teardown list — the release sweep runs leaf
// to root, so the process stops before the mounts it reads and the mounts
// before the netns they live in. The same chain, swept forward, is bringup.
func ExampleChain() {
	chain, apply := docStackChain()

	world := DocStack{hasNet: true, hasMounts: true, proc: "running"} // deleting

	for range 8 {
		done, want := chain(world)
		if done {
			fmt.Println("released")

			break
		}

		fmt.Println(want[0])
		apply(&world, want[0])
	}

	// Output:
	// SIGTERM_PROCESS
	// UNMOUNT_VOLUMES
	// DELETE_NETNS
	// released
}

// The same chain, brought up: the acquire sweep runs root to leaf, so the
// process starts only after the netns and mounts hold.
func ExampleChain_bringup() {
	chain, apply := docStackChain()

	world := DocStack{wantNet: true, wantMounts: true, wantProc: "running"} // a fresh host, wanting a stack

	for range 8 {
		done, want := chain(world)
		if done {
			fmt.Println("converged")

			break
		}

		fmt.Println(want[0])
		apply(&world, want[0])
	}

	// Output:
	// CREATE_NETNS
	// MOUNT_VOLUMES
	// START_PROCESS
	// converged
}

// Independent stages ship their effects in one pass — three image pulls are
// one batch, not one waiting behind another.
func ExampleConcurrent() {
	world := World{WantNet: true, WantUnit: "running"}

	stage := teleos.Concurrent(PodInvariants...)
	_, want := stage(world)

	fmt.Println(want)

	// Output:
	// [NETNS_CREATE UNIT_START]
}
