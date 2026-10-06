// Copyright (C) 2025-2026 Malformed C. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package teleos

// Invariant evaluates one slice of reality against its resting state. It
// returns (true, nil) when the observation satisfies equilibrium, or
// (false, want) where want is the minimal list of data effects that would
// move reality toward it.
//
// An invariant is a sensor and a restoring force, and nothing else: it reads
// only the observation it is handed, touches nothing, and terminates. It must
// give the same answer for the same observation every time — the engine's
// diagnoses (stall, oscillation) are only as sound as that purity.
type Invariant[S, E any] func(state S) (done bool, want []E)

// Plan composes invariants in slice order and halts at the first unsatisfied
// one — the convergence frontier. A later invariant is never evaluated while
// an earlier one stands open, so slice order is dependency order: no
// After: []string graph, and no way to apply a downstream effect onto an
// unready foundation.
//
// The frontier is also the health signal: which invariant is unsatisfied is
// what a converged-but-silent system has to say. The Engine reports the index.
func Plan[S, E any](invariants ...Invariant[S, E]) Invariant[S, E] {
	return func(state S) (bool, []E) {
		for _, inv := range invariants {
			done, want := inv(state)
			if !done {
				return false, want
			}
		}

		return true, nil
	}
}

// Rule pairs a pure condition with a restoring force: when done holds, the
// rule is satisfied; when it does not, the rule emits want.
//
// It is the general form — want may be any length, which is how one invariant
// converges a gap that takes several effects at once (three missing
// environment variables, a mount and its unit).
func Rule[S, E any](done func(s S) bool, want func(s S) []E) Invariant[S, E] {
	return func(s S) (bool, []E) {
		if done(s) {
			return true, nil
		}

		return false, want(s)
	}
}

// Align eliminates the gap between desired intent and observed reality for one
// comparable scalar of state: when desired and observed differ, the restore
// force produces the single effect that closes the gap.
//
// comparable buys cheap equality and costs expressiveness: it ends at enums,
// bools, and strings. A gap over structured state — a spec with ten fields, of
// which three drifted — needs a diff, not ==, and should be written as a Rule
// whose want derives the whole delta.
func Align[S, E any, T comparable](
	desired func(s S) T,
	observed func(s S) T,
	restore func(target T, s S) E,
) Invariant[S, E] {
	return func(s S) (bool, []E) {
		target, actual := desired(s), observed(s)
		if target == actual {
			return true, nil
		}

		return false, []E{restore(target, s)}
	}
}
