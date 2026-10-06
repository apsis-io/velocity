# teleos

Level-triggered invariant convergence for Go. A system is described by the
resting state it must hold — its *telos* — not by the steps that get it there.

```go
engine := teleos.New(teleos.Config[World, Effect]{
    Plan: []teleos.Invariant[World, Effect]{net, unit}, // slice order is dependency order
})

report := engine.Step(observe())       // observe: reality into a struct
if report.Status == teleos.Frontier {
    apply(report.Want)                 // pure data effects; then observe again
}
```

An `Invariant[S, E]` is a pure function from an observation to two facts: is
this slice of reality at rest (`done`), and if not, what minimal data effects
(`want []E`) would move it there. No I/O, no clocks, no memory of previous
passes. From that one shape, three properties fall out for free:

- **Crash recovery is the same evaluation as steady state.** The engine holds
  no execution trace; a rebooted daemon evaluates the plan and continues from
  whatever survived.
- **Self-healing is gravity.** An administrator deletes the interface; the
  next observation re-opens the gap; the restoring force fires again.
- **The core tests in nanoseconds.** `(done, want)` asserted against a
  struct — nothing mocked, nothing slept.

Composition covers the three real shapes of a dependency graph. `Plan`
serialises — slice order is dependency order. `Concurrent` runs independent
stages in one pass, aggregating their effects into a single batch. `Chain`
is for resources that both acquire and release — a netns under a mount under
a process: the acquire sweep runs root to leaf, the release sweep runs leaf
to root, and the direction of every pass comes from the chain itself. There
is no phase flag: deleting the pod stops the process before the netns it
lives in disappears because nothing may remove itself while something stands
on it — a structural truth, not a mode.

Name the stages and the report becomes a status document:

```go
engine := teleos.New(teleos.Config[World, Effect]{
    Plan:  []teleos.Invariant[World, Effect]{net, mounts, process},
    Names: []string{"NetworkReady", "MountsReady", "ContainersReady"},
})

// report.Stage == "MountsReady", report.Frontier == 1, and
// names[:report.Frontier] is what already converged — PodConditions,
// per-stage metrics, and progress bars are projections of one report.
```

An honest note on what this library is: the invariants are closures over
`if` statements, and a single pure `func Decide(w World) []Effect` with the
ifs written by hand is just as pure and just as testable. What the ifs do
not give you — what no hand-rolled decide function gives you — is the
machine around them: budgets that bound a broken loop in operations, stall
and oscillation diagnoses with the stuck stage named, and a plan structure
that reports its own progress. If you do not need those, write the ifs. If
your daemon needs them, they are this.

The [engine](doc.go) is the interesting part: a pure pass machine that bounds
the loop's failure modes in operations, never in time — a per-pass effect cap
(a plan that never stops emitting is a wedge, and a wedge is never handed to
an executor), a pass budget, stall detection (nothing moves), and oscillation
detection (the plan fights itself, A-B-A). Terminal diagnoses are data, and
`Reset` is the human's decision.

## Running it on velocity's runtime

`teleos` itself imports nothing. [`converge`](converge) is the operational
half: one goroutine waiting for level-triggered wakes, observing, applying
effects through a bounded `async.Runner` batch, coalescing repeats through
`dedupe`, reporting diagnoses to the daemon.

```go
c, _ := converge.New(engine, observe, apply, converge.Config[World, Effect]{
    Runner:   runner,            // async.Runner: bounded, barriered batches
    Coalesce: group, Key: key,   // dedupe: two loops wanting one unit join forces
    OnReport: report,            // terminal diagnoses surface here
    OnError:  onErr,             // transient failures: wait for the next wake
})

for { c.Wake(); <-something }  // Wake from watches; timers are the caller's
```

The velocity-native effect type is `opcodes.Instruction`: a plan emits pure,
unexecuted data (`Op` plus operand slots), and an `opruntime.Table` is their
only executor — `converge.Dispatch(table)` is the apply function. Handlers in
the table are where op composition lives.

The division of labor with [velocity](https://github.com/apsis-io/velocity):
teleos is the mind and touches nothing; `ownership.Scope` unwinds
partially-applied effect batches, `dedupe` coalesces, `async.Runner` bounds,
`opruntime` dispatches, and `lostrelease` — the leak analyzer — checks the
handlers, because effects allocate handles and the next release of velocity
nets the ones whose release gets lost.

## Status

New, and evaluated: [barrier_eval_test.go](barrier_eval_test.go) re-expresses
perigeos's coordinated-checkpoint controller — a hand-rolled four-phase
state machine with a no-outage barrier — as one plan, and asserts the
controller's properties structurally: nothing crosses the barrier while any
member is un-quiesced, members progress independently, the wall-clock
timeout is an observed fact, and `status.phase` falls out of the stage
names. The real migration is the next step, and the evaluation is its
specification.
