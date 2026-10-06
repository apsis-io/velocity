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

New. The engine's contract is [documented](doc.go) and the tests pin the
design-doc transcript to the pass. The first real plan is the perigeos
reconcile site — that migration is the evaluation the library has to pass.
