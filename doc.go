// Package velocity is a set of Go 1.27 concurrency foundations. The root
// package holds only the version; the work is in the subpackages, chosen by
// the shape of the problem:
//
//   - ownership — deterministic cleanup and handoff for a value: when Drop
//     runs, who may touch the value meanwhile, and transfer between
//     goroutines. Borrow checks are an assertion layer, never a wait.
//     Start with Own, NewCloser, Lease, Scope, or Frozen.
//   - pool — a bounded set of made, held, and returned resources; checkouts
//     are ownership leases.
//   - async — bounded fan-out: a Runner states a Limit once and runs labeled
//     tasks (Gather, Race, FirstSuccess), a function over a collection (Map,
//     ForEach), workers over one owned value (Broadcast), or an ErrGroup
//     that replaces x/sync/errgroup. Pipeline chains typed stages; Group
//     tracks goroutines with panic recovery.
//   - dedupe — coalesces concurrent calls per key so one runs and every
//     caller gets the result; results become owned handles only on request.
//   - resilience — Retry with classifiers, backoff, and an injectable Clock;
//     a circuit Breaker that never waits; ManualClock for tests of either.
//   - opcodes, opruntime — plain data shapes for operations and a registry
//     mapping them to Go functions. Not a VM.
//   - traits — the Drop and Clone function types ownership is configured
//     with, composable as Drop(...).Clone(...).
//
// The analysis module (a separate go.mod, so the library does not depend on
// x/tools) ships lostrelease, a go vet analyzer that reports an ownership
// borrow, lease, or pool checkout not released on every path.
//
// Every package follows the same rules: nothing waits except where the
// caller's context governs it, errors are typed and unwrap to sentinels,
// callbacks return normally, and instrumentation is a Hooks struct the
// caller supplies rather than metrics the package keeps.
package velocity
