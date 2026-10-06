// Copyright (C) 2025-2026 Malformed C. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package teleos

// Resource is one physical resource in a dependency chain: a netns under a
// mount under a process. Exists reports whether the resource is physically
// present on the host right now — the one fact the atom deliberately does
// not carry, because existence is the author's structural knowledge of the
// chain, not something every invariant can derive from its own gap. Its
// Invariant is the resource's equilibrium, closing its gap in whichever
// direction the observation demands.
type Resource[S, E any] struct {
	Exists    func(S) bool
	Invariant Invariant[S, E]
}

// Chain orders a dependency sequence whose links acquire and release, and
// derives the direction of every pass from the chain itself — there is no
// phase flag, because there is no phase. The order is two local truths:
//
//   - A link may come into being only once everything beneath it already
//     holds. The acquire sweep runs root to leaf and halts at the first gap.
//   - A link may remove itself only once everything standing on it has
//     already gone. The release sweep runs leaf to root first, so the
//     highest pending release preempts everything else — deleting a pod
//     stops the process before the netns it lives in disappears, with no
//     mode switch and no human telling the plan which direction it is going.
//
// That order is a property the chain owns. Before it, an ordering like this
// was enforced by a source grep — a test asserting the positional
// relationship of two call sites. Sweep direction is stronger: the mutation
// that breaks the order emits its violation against a live dependency, and
// the behavioral test names what happened.
//
// A link that exists but is not at rest is treated as a release too: an
// in-place change to a foundation preempts building downstream on it, which
// is the conservative reading of "the foundation is moving".
//
// The sweeps are independent per link, so a chain can acquire some links
// while releasing others — a rolling replacement is one chain doing both,
// ordered correctly in each direction by the same two rules.
//
// Intent that contradicts the chain — keep the process but delete the netns
// it lives in — is not an ordering error; it is unsatisfiable, and the
// model answers the way physics does: the effect fails, the passes burn the
// budget, and Exhausted names the link that could not converge. The chain
// cannot save a plan from wanting something impossible; it only refuses to
// make the impossible destructive.
func Chain[S, E any](resources ...Resource[S, E]) Invariant[S, E] {
	return func(s S) (bool, []E) {
		// Release first, leaf to root: what stands above must be gone
		// before what stands below may go.
		for i := len(resources) - 1; i >= 0; i-- {
			done, want := resources[i].Invariant(s)
			if !done && resources[i].Exists(s) {
				return false, want
			}
		}

		// Then acquire, root to leaf: what stands below must hold before
		// what stands above may come into being.
		for i := range resources {
			done, want := resources[i].Invariant(s)
			if !done {
				return false, want
			}
		}

		return true, nil
	}
}
