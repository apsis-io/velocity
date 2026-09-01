# velocity

Go 1.27 concurrency foundations: deterministic cleanup and handoff,
bounded fan-out, request coalescing, and retry/circuit-breaker policies —
built on Go 1.27 generic methods, with a `go vet` analyzer that catches
leaked handles before the code runs.

```sh
go get github.com/apsis-io/velocity@v0.2.0
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

## Performance

Head-to-head numbers against the libraries these packages drew from —
`x/sync`, `go-singleflightx`, `resenje.org/singleflight`, `hunch`, `conc` —
with fairness rules and an honest account of where velocity is slower and
why, live in [`benchmarks/README.md`](benchmarks/README.md).

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

Apache License 2.0 — see [LICENSE](LICENSE) and [NOTICE.md](NOTICE.md).
