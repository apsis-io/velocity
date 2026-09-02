# velocity

[![CI](https://github.com/apsis-io/velocity/actions/workflows/ci.yml/badge.svg)](https://github.com/apsis-io/velocity/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/apsis-io/velocity.svg)](https://pkg.go.dev/github.com/apsis-io/velocity)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue)](LICENSE)

Go 1.27 concurrency foundations: deterministic cleanup and handoff,
bounded fan-out, request coalescing, and retry/circuit-breaker policies —
built on Go 1.27 generic methods, with a `go vet` analyzer that catches
leaked handles before the code runs.

```sh
go get github.com/apsis-io/velocity@v0.4.0
```

**Status: v0.** Requires Go 1.27. The API is deliberate but young — there
are no external consumers yet, and it will change where real use says it
should. Every design decision and its reasoning is recorded in
[`docs/decisions.md`](docs/decisions.md).

## Which package

| you have | use |
|---|---|
| a resource whose cleanup must run exactly once, at a known time | [`ownership`](#ownership) |
| a bounded set of connections, buffers, or handles to reuse | [`pool`](#pool) |
| N tasks or a collection to run concurrently, bounded | [`async`](#async) |
| many concurrent callers wanting the same expensive result | [`dedupe`](#dedupe) |
| a flaky dependency to retry or stop calling | [`resilience`](#resilience) |

Every package follows the same rules: **nothing waits** except under the
caller's context; errors are typed and `errors.Is` to sentinels; callbacks
return normally; instrumentation is a `Hooks` struct the caller supplies,
not metrics the package keeps.

## ownership

`ownership` decides when cleanup runs and makes transfer between goroutines
explicit. Its borrow checks are an assertion layer: a conflicting access is
reported at once as `ErrConflict`, never waited out, so nothing in the
package can deadlock. It is not Rust ownership and not a mutex — use a plain
`defer Close()` or an `RWMutex` where those are the right tool.

```go
conn := ownership.NewCloser(rawConn)   // Drop is Close
defer conn.Close()

cfg := ownership.Own(config)            // no Drop; borrow-checked
name, err := cfg.View(func(c Config) (string, error) { return c.Name, nil })
err = cfg.WithWrite(func(c *Config) error { c.Retries++; return nil })
```

The shapes that pay for themselves:

- **`Scope`** unwinds a multi-step construction that fails partway, so each
  acquisition no longer closes everything opened before it:
  ```go
  scope := ownership.NewScope()
  defer scope.Close()                      // LIFO, continues past failures, errors joined
  conn, err := dial(); if err != nil { return nil, err }
  _ = scope.OwnCloser(conn)
  raw, err := dial();  if err != nil { return nil, err }   // Close releases conn
  _ = scope.OwnCloser(raw)
  scope.Disarm()                           // the bundle owns them now
  return &Bundle{conn, raw}, nil
  ```
- **`Lease`** for resources identified by a value — permits, IP allocations —
  enforcing release-exactly-once and use-after-release.
- **`Frozen`** publishes a value read-only *by type*: there is no `Mutate` to
  call. `Viewer[T]`/`Mutator[T]` put the same guarantee in any signature.
- **`Seal`/`Drained`** retire a value other goroutines are still reading
  without waiting inside the package:
  ```go
  owner.Seal()
  select {
  case <-owner.Drained():
  case <-ctx.Done():
      return ctx.Err()
  }
  return owner.Release()
  ```
- **`Map`** transforms an owned value into another type while chaining Drop
  (flush the writer, then close the file). **`Detach`** is the one exit that
  skips Drop, named for what it does to cleanup responsibility.

Model, invariants, and the "when not to use this" list:
[`docs/ownership.md`](docs/ownership.md). Full API:
[`docs/ownership-cheatsheet.md`](docs/ownership-cheatsheet.md).

### The leak check is static

The [`analysis`](analysis) module (separate `go.mod`, so the library does not
depend on `x/tools`) ships **`lostrelease`**, a `go vet` analyzer modelled on
`lostcancel`: a `Borrow`, `BorrowMut`, `NewLease`, or `pool.Get` handle that
is discarded, or has a path to a return on which it is never released, is
reported at the acquisition and at the return.

```sh
go -C analysis build -o /tmp/velocityvet ./cmd/velocityvet
go vet -vettool=/tmp/velocityvet ./...
```

It learns what to track from the code rather than from a list it carries.
A function that hands back something the caller must release says so at its
own declaration:

```go
// Borrow acquires an advanced shared read borrow.
//
//velocity:acquires
func (o *Owner[T]) Borrow() (*ReadBorrow[T], error)
```

Go treats that as a directive, so godoc hides it. The analyzer publishes it
as a package fact, which reaches consumers — who see velocity through
export data and never its comments. **Any** library can mark its own
handle-returning functions and get the same checking; nothing about the
mechanism is velocity-specific.

Production `Borrow` carries no runtime safety net — a leaked borrow blocks
its cell deterministically rather than being reclaimed at some GC-chosen
moment. Under `-tags=velocitydebug` a leak is logged through `slog` and
released so tests keep going.

## pool

A bounded set of made, held, and returned resources. A `Checkout` *is* an
`ownership.Lease`, so use-after-return and double return are caught, and it
is an `io.Closer` a `Scope` can own.

```go
clients, err := pool.New(pool.Config[*Client]{
    New:   dial,
    Close: func(c *Client) error { return c.Close() },
    Max:   8,
})

checkout, err := clients.Get(ctx)     // waits for capacity under ctx
defer checkout.Release()              // or checkout.Discard() if it turned out broken
client, err := checkout.Value()
```

## async

A `Runner` states a concurrency policy once — an explicit `Limit` (there is
no implicit "unbounded" default) and optional `Hooks` — and every operation
runs through it.

```go
run, err := async.New(async.Limited(8), async.WithHooks(hooks))

outcomes, err := run.Gather(ctx, async.Named("a", fetchA), async.Named("b", fetchB))
outcomes, err = run.GatherFuncs(ctx, fetchA, fetchB)   // unlabeled
first, err := run.FirstSuccess(ctx, tasks...)           // Race for first completion
results, err := run.Map(ctx, items, process)            // fixed pool of Limit workers, []R in order
err = run.ForEach(ctx, items, visit)
```

`Gather` returns outcomes in source order with every error joined. `Map`
returns a bare `[]R`; failures travel out of band as one `*ItemError{Index,
Err}` per failed item in the joined error, and a failed item's slot is the
zero value. `Broadcast` fans one owned value out to workers under concurrent
read borrows; `Pipeline` chains typed stages via a generic `Then[R]`; `Group`
wraps `sync.WaitGroup.Go` with panic recovery.

### Replacing `x/sync/errgroup`

`run.ErrGroup(ctx)` is `errgroup.WithContext` with the pieces it lacks:

```go
eg, ctx := run.ErrGroup(ctx)              // Limit from the Runner; bounds goroutines
for _, item := range items {
    eg.Go(func(ctx context.Context) error { return process(ctx, item) })
}
err := eg.Wait()                          // first error; siblings were cancelled on it
```

| | `errgroup` | `async.ErrGroup` |
|---|---|---|
| context | closed over | passed to each function |
| limit | `SetLimit` per group | stated once on the `Runner` |
| a panic | crashes the process (or re-panics in `Wait`) | recovered into a `*Panic` error |
| after the first failure | later functions still run | not run |
| all errors | first only | `Wait` first, `Errors()` all, in order |
| a function that ignores cancellation | `Wait` hangs | `WaitContext(ctx)` bounds it |
| instrumentation | none | `Hooks` see wait and run time |
| typed results | index bookkeeping by hand | `Gather` / `Map` |
| cost, 8 functions | 3.2 µs / 20 allocs | 3.4 µs / 12 allocs; at a limit of 4, 4.8 vs 4.8 |

Two things `errgroup` gave implicitly that the replacements do not:

- **One error at a boundary.** `Map` joins an `*ItemError` per failure, which
  is more information than a 1 KB status field wants when every item failed
  for the same reason. `ErrGroup.Wait` is first-error-only; for `Map`,
  `async.Failures(err)[0]` is the lowest failed item.
- **Every submission runs.** `errgroup` ran every function it started, so
  cleanup inside a function for state set up outside it was unconditional.
  `Map` does not run an item never claimed after cancellation, and
  `ErrGroup` does not run a function that gets its permit after the first
  failure. Cleanup for state registered *before* the fan-out therefore
  belongs in `Hooks.OnTaskComplete` (which `Map` fires for unclaimed items
  with the cause) or in a sweep after the call, not inside the function.

### Cancellable locking

`sync.Mutex` cannot be locked under a context, which is what
`x/sync/semaphore.NewWeighted(1)` usually stands in for. `async.Mutex` and
`async.Semaphore` wait under the caller's context and hand back a `Permit`
that is released exactly once; a permit that is not released is reported by
`lostrelease`, including the `Try` forms' `ok` branch.

```go
held, err := mu.Lock(ctx)
if err != nil {
    return err
}
defer held.Release()
```

## dedupe

`Group[K, V]` coalesces concurrent calls per key: one runs, every caller
gets the result. The zero value works; `New` takes options. A caller that
leaves does not cancel the work unless it was the last one — and even then
the key stays registered until the callback returns, so a callback that
ignores its context never stacks: a later caller waits for it and takes its
value if it succeeded, or starts afresh if it failed.

```go
group, err := dedupe.New[string, Report]()

report, err := group.Do(ctx, "report-42", func(ctx context.Context) (Report, error) {
    return fetchReport(ctx, 42)
})
```

The context `fn` receives is the round's and is cancelled when the round
completes, so a value that keeps doing I/O after `Do` returns (a lazy
handle, a stream) must not be built from it.

`DoBatch` runs one function over several keys; `DoBorrowed` loans an owned
input to the round. When the result is a resource, configure
`WithResultDrop` and use `DoShared`: every caller gets a counted
`*ownership.Shared[V]`, and Drop runs once, after the last release.
`Singleflight` is an alias for readers who know the pattern by that name.

## resilience

```go
backoff, err := resilience.ExponentialBackoff(100*time.Millisecond, 5*time.Second, 0.2)
value, err := resilience.Retry(ctx, resilience.Policy{MaxAttempts: 5, Backoff: backoff}, fetch)

breaker, err := resilience.NewBreaker(resilience.BreakerPolicy{
    Trip:    resilience.FailureRatio(0.5, 20),
    OpenFor: 30 * time.Second,
    Failure: func(err error) bool { return !errors.Is(err, context.Canceled) },
})
value, err = breaker.Do(ctx, fetch)          // errors.Is(err, resilience.ErrOpen) when tripped
```

A tripped breaker rejects immediately and recovers on the clock; nest it
inside `Retry` with `ErrOpen` classified as not retryable. `ManualClock`
makes tests of either deterministic.

`Hedge` is the tail-latency counterpart of `Retry`: `Retry` waits for an
attempt to fail and cannot help one that is merely slow, while `Hedge`
starts the next attempt while the previous is still running, so p99 falls
toward p50. The first success wins and the rest are cancelled.

```go
value, err := resilience.Hedge(ctx, resilience.HedgePolicy[*Response]{
    MaxAttempts: 3,
    Delay:       backoff,                       // the same Backoff Retry uses
    Budget:      budget,                        // caps the extra load hedging creates
    Discard:     func(r *Response) error { return r.Body.Close() },
}, func(ctx context.Context, attempt int) (*Response, error) {
    return client.Fetch(ctx, request)
})
```

`Discard` disposes the losing results a hedge inherently produces: N
attempts return one, so the rest leak unless something closes them. Not
every hedging library omits this — `faustbrian/go-hedge` has a `Disposer`
and velocity took the idea from it — but failsafe-go does, and where a
library treats the result as opaque the cleanup has to be written out a
second time. When the result is owned, `Discard` is simply its `Drop`. `Budget` is the other half — a dependency slow enough to trigger
hedging is the last one that should receive several times its usual load, so
each execution credits the budget and each speculative attempt spends a
credit.

`Delay` can be measured rather than guessed: `NewLatencyDelay(0.95, …)`
hedges an attempt once it exceeds the p95 of recent successful ones, so
"too slow" tracks what the dependency has actually been doing.

### On failsafe-go

`resilience` is three policies, not a resilience framework, and that is the
whole of it. [failsafe-go](https://github.com/failsafe-go/failsafe-go)
covers much more — timeout, fallback, rate limiting, bulkhead, adaptive
concurrency and throttling, HTTP and gRPC integrations — is actively
maintained, and is the better choice when breadth is what you want. velocity
does not try to catch up with it.

What failsafe-go cannot express is that a **result may own something**. Every
policy that discards a result leaks it when the result is a connection, a
lock, a file handle, or filesystem artifacts such as a temp directory and
the blob inside it: a hedge drops N−1 results, a retry with a result
predicate discards one that arrived perfectly well, a fallback drops the
primary's, a timeout returns before a result that arrives anyway. That is
not a bug in failsafe-go — a library that treats the result as opaque cannot
know that dropping one costs something.

The [`failsafeown`](failsafeown) module supplies the missing half. Make the
result an `*ownership.Owner[T]`, which carries its own `Drop`, and whatever
the policy chain does not hand back is released:

```go
exec := failsafe.With(
    fallback.NewWithResult(spare),
    hedgepolicy.NewWithDelay[*ownership.Owner[*Conn]](50*time.Millisecond),
)
conn, err := failsafeown.Get(ctx, exec, dial, failsafeown.Hooks[*Conn]{})
// every connection the chain dialled and dropped is closed
```

`GetWithExecution` is the same thing for attempts that are **not**
interchangeable, which for a hedge is the common case since replicas
usually have addresses — racing a peer against an origin needs to know
which attempt it is:

```go
policy := hedgepolicy.NewBuilderWithDelay[*ownership.Owner[*Layer]](0).
    // Without this, failsafe cancels the race on ANY result, so the arm
    // that fails fastest ends the one that would have succeeded.
    CancelIf(func(_ *ownership.Owner[*Layer], err error) bool { return err == nil }).
    Build()

owner, err := failsafeown.GetWithExecution(ctx, failsafe.With(policy),
    func(e failsafe.Execution[*ownership.Owner[*Layer]]) (*ownership.Owner[*Layer], error) {
        if e.IsHedge() {
            return fetchFromRegistry(e.Context())
        }
        return fetchFromPeers(e.Context())
    }, hooks)
```

Counting attempts in the closure instead works only while nothing else in
the chain also reruns `fn`; add a retry and the counter silently stops
meaning what it did.

Use `IsHedge`, **not** `Hedges`, to tell the arms apart. `Hedges` is a count
shared by every attempt — "how many hedges exist", in-progress ones
included — so with a short delay both arms read the same number, both take
the same branch, and the branch nobody took never runs. That is a hang
rather than a wrong answer, and it does not reproduce under a long delay,
because then the primary reads the counter before the hedge exists.

It is a separate module, so the library itself keeps no dependency on
failsafe-go.

## Performance

Head-to-head numbers against the libraries these packages drew from —
`x/sync`, `go-singleflightx`, `resenje.org/singleflight`, `hunch`, `conc` —
with fairness rules and an honest account of where velocity is slower and
why, live in [`benchmarks/README.md`](benchmarks/README.md).

## Field use

One consumer so far, which is the honest scope. Periapsis, a
virtual-kubelet fork, ported seven call sites to velocity and has tracked
each release since. It exercises `ownership`, `async` (`Runner.Map`,
`ErrGroup`, `Mutex`), `dedupe`, and `failsafeown`, and dropped `conc`,
`x/sync/singleflight` and `x/sync/errgroup` on the way; v0.4.0 is deployed
on its cluster.

Most of what changed in v0.2.0 and v0.3.0 came from that port, and the
reports were measured rather than impressionistic:

- **`dedupe` holding an abandoned round's key until its callback returns**
  took a ping loop from 10 in-flight calls per second to 1, matching what
  `x/sync/singleflight` does for a callback that ignores its context.
- **The zero-value `dedupe.Group`** exists because a partial struct literal
  holding one compiled and then nil-panicked on the first uncached call,
  where `singleflight.Group` had worked uninitialised for years.
- **`failsafeown.GetWithExecution`** exists because the module could not
  express a hedge racing a peer against a registry — the case its own
  documentation led with.
- **`async.Mutex`'s benchmark note** is that consumer's measurement,
  including the part that says the end-to-end difference was below
  run-to-run variance.

The same port also corrected this repository's claims more than once, and
[`docs/decisions.md`](docs/decisions.md) records which were wrong and why
rather than quietly restating them.

## Development

```sh
just check      # fmt, vet + staticcheck, lint (lostrelease), test, race, velocitydebug
just fuzz       # ownership state-machine model, 30s
just bench      # in-module benchmarks
just bench-compare
```

`just` is optional; each recipe is a plain `go` command listed in the
[`justfile`](justfile).

## License

Copyright 2026 Malformed C. Licensed under the Apache License, Version 2.0;
you may not use these files except in compliance with it. See
[LICENSE](LICENSE) for the terms and [NOTICE.md](NOTICE.md) for the
attributions — velocity is an independent implementation informed by
several libraries, none of whose source it bundles.
