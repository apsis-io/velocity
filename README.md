# velocity

Experimental Go 1.27 concurrency foundations: deterministic cleanup and
handoff (`ownership`, `pool`), bounded task execution (`async`), request
coalescing (`dedupe`), and retry/circuit-breaker policies (`resilience`),
built on Go 1.27 generic methods throughout.

## Ownership

`ownership` decides when cleanup runs and makes transfer between goroutines
explicit. Its borrow checks are an assertion layer: a conflict is reported at
once as `ErrConflict`, never waited out, so nothing in the package can
deadlock. It is not Rust ownership and not a mutex; use a plain `defer
Close()` or an `RWMutex` where those are the right tool. See
[`docs/ownership.md`](docs/ownership.md) for the model and
[`docs/ownership-cheatsheet.md`](docs/ownership-cheatsheet.md) for the API.

## Resource patterns

`NewCloser` owns an `io.Closer`; `View`/`Mutate` (and their error-only forms
`WithRead`/`WithWrite`) touch the value under a borrow that lasts exactly as
long as the call:

```go
conn := ownership.NewCloser(rawConn)
defer conn.Close()

name, err := cfg.View(func(c Config) (string, error) { return c.Name, nil })
err = cfg.WithWrite(func(c *Config) error { c.Retries++; return nil })
```

Concurrent `View`s coexist; a `Mutate` meanwhile returns `ErrConflict`
immediately. A returned error does not roll a mutation back.

`Scope` unwinds a multi-step construction that fails partway, so each
acquisition no longer has to close everything opened before it:

```go
scope := ownership.NewScope()
defer scope.Close()

conn, err := dial()          // scope.Close closes conn if a later step fails
if err != nil {
    return nil, err
}
_ = scope.OwnCloser(conn)

raw, err := dial()
if err != nil {
    return nil, err
}
_ = scope.OwnCloser(raw)

scope.Disarm()               // the bundle below owns them now
return &Bundle{conn: conn, raw: raw}, nil
```

Release runs in reverse order, continues past failures, and joins the errors.

`Lease` covers resources identified by a value rather than represented by
one — permits, IP allocations, unit references. It enforces
release-exactly-once and catches use-after-release, but is deliberately not
borrow-checked:

```go
lease, err := ownership.NewLease(ip, pool.Release)
defer lease.Release()

addr, err := lease.Value()   // ErrReleased once handed back
```

To retire a value other goroutines are still reading, `Seal` stops new borrows
and `Drained` reports when the in-flight ones have finished. The waiting is
yours, so nothing in the package ever blocks:

```go
conn.Seal()
select {
case <-conn.Drained():
case <-ctx.Done():
    return ctx.Err()
}
return conn.Release()
```

`pool.Pool[T]` puts `Lease` to work for resources that are made, held, and
returned — connections, buffers, handles. `Get` waits for capacity under the
caller's context; a `Checkout` *is* a `Lease`, so use after return and
double return are caught, and `Discard` closes a broken resource instead of
returning it. It is also an `io.Closer`, so a `Scope` can own it:

```go
clients, err := pool.New(pool.Config[*Client]{
    New:   dial,
    Close: func(c *Client) error { return c.Close() },
    Max:   8,
})

checkout, err := clients.Get(ctx)
defer checkout.Release()          // or checkout.Discard() if it turned out broken
client, err := checkout.Value()   // ErrReleased once returned
```

## Freeze and transform

`Freeze` gives up mutation for a counted read-only handle. `Frozen[T]` has no
`Write`, `BorrowMut`, or `Update` to call, so this is enforced by the type
rather than rejected at runtime; `IntoOwner` thaws the sole unborrowed handle
back:

```go
frozen, err := owner.Freeze()
peer, err := frozen.Clone()      // counted, like Shared
defer peer.Release()
defer frozen.Release()
```

`Map` transforms an owned value into another type while keeping cleanup
intact — unlike `Detach`, which exits ownership without running Drop:

```go
writer, err := file.Map(
    func(f *os.File) (*bufio.Writer, error) { return bufio.NewWriter(f), nil },
    ownership.WithDrop(func(w *bufio.Writer) error { return w.Flush() }),
)
// Releasing writer flushes it, then closes the file underneath.
```

The callback must not close the source value itself: the source Drop still
runs afterwards.

## Transfer, sharing, and Drop

```go
moved, err := owner.Move()       // old owner becomes moved
value, err := moved.Detach()     // exits ownership; Drop never runs

owner, _ = ownership.New(value)
shared, err := owner.IntoShared()
peer, err := shared.Clone()      // explicit counted handle
_ = peer.Release()
owner, err = shared.IntoOwner()  // succeeds only when sole and unborrowed
```

`Release` and `Close` are exact aliases and idempotent. `Shared` exists for
one reason: `Drop` runs when the *last* handle releases, deterministically,
which the garbage collector cannot promise. Assigning a `*Shared` does not
count a handle — call `Clone`.

```go
owner, err := ownership.New(
    []byte("velocity"),
    ownership.WithDrop(func(value []byte) error { clear(value); return nil }),
    ownership.WithClone(func(value []byte) ([]byte, error) { return bytes.Clone(value), nil }),
)
copy, err := owner.Snapshot()
```

Drop runs at most once on final release; its first error is returned once
and retained by `State`. A Clone is only as independent as its
implementation; velocity cannot validate clone quality.

## Advanced borrows

For a borrow that must span a call boundary — a goroutine, a `dedupe` round.
Always release; a leaked borrow blocks its cell until released.

```go
borrow, err := owner.Borrow()
defer borrow.Release()

first, err := borrow.Project(func(values []int) (int, error) { return values[0], nil })
```

Multiple reads coexist. A write borrow, move, or release during a read
returns `ErrConflict` immediately. With `-tags=velocitydebug`, a borrow
that becomes unreachable while held is logged through `slog.Default()` and
released; production builds have no such net.

## Safety boundary

Borrow state is enforced at runtime, on handles, not on data. A map, slice,
pointer, or interface reached through a callback remains usable after the
borrow ends, and nothing can revoke it. The guarantee is therefore exact for
value types and porous for reference types; use a correct Clone and
`Snapshot` when isolation matters.

All user callbacks, Clone, and Drop functions must return normally. They must
not panic or call `runtime.Goexit`; returned errors are the supported failure
channel. Deferred scoped cleanup still releases borrows if a violating callback
panics.

## Opcodes and opruntime

`opcodes` defines plain data shapes for identifying an operation and its
operands. It performs no encoding and defines no domain-specific operations;
a caller declares its own `Op` constants:

```go
const opPrint opcodes.Op = 1
```

`opruntime` is glue: a registry mapping an `opcodes.Op` to the Go function
that implements it, plus dispatch. It is not a virtual machine; it owns no
registers, stack, or execution state.

```go
table := opruntime.NewTable()
err := table.Register(opPrint, func(inst opcodes.Instruction) error {
    fmt.Println("printed", inst.A)
    return nil
})

err = table.Dispatch(opcodes.Instruction{Op: opPrint, A: 42})
```

`opruntime.Run` dispatches a `[]opcodes.Instruction` in order and stops at
the first error; it is a thin convenience loop around `Table.Dispatch`, not
a bytecode program format. Neither package depends on `ownership`.

## Dedupe

`dedupe.Group[K, V]` suppresses duplicate concurrent work per key and shares
the result as an `*ownership.Shared[V]` handle — every caller of a dedup
round gets its own independently released clone:

```go
group, err := dedupe.New[string, Report](ctx)

shared, err := group.Do(ctx, "report-42", func(ctx context.Context) (Report, error) {
    return fetchReport(ctx, 42)
})
defer shared.Release()
```

Loan an owned input to the round with `DoBorrowed` or `DoBorrowedMut`; the
leader holds the borrow until its callback returns, and the loan is released
before the result becomes visible. If the caller context is canceled while
non-cooperative work continues, the input loan may remain held until the
callback returns; `WithHooks`/`OnComplete` can signal when it is reusable again:

```go
input, err := ownership.New(request)
shared, err := group.DoBorrowed(ctx, "report-42", input,
    func(ctx context.Context, request Request) (Report, error) {
        return buildReport(ctx, request)
    })
```

`Forget` stops tracking a key without interrupting work already in flight;
`Cancel` actively cancels it. `DoBatch` runs one function over several keys
and aligns the result map to the request, reporting `ErrMissingResult` for
any key the function didn't return.

The registry backend is constructor-selectable. `WithXsyncBackend` is the
default and scales best when many goroutines register distinct keys;
`WithMutexBackend` is faster uncontended and allocates one less per call;
`WithSharded(n)` sits between them. See
[`benchmarks/README.md`](benchmarks/README.md) for the numbers behind that.

`WithHooks` lets a caller supply their own instrumentation — `dedupe`
doesn't collect metrics itself, it just calls back synchronously at points
a caller can't observe by timing their own function, such as a follower's
`OnComplete` duration reflecting the leader's actual work, not the
follower's wait:

```go
group, err := dedupe.New[string, Report](ctx, dedupe.WithHooks(dedupe.Hooks[string]{
    OnComplete: func(key string, duration time.Duration, err error) {
        reportLatency(key, duration, err)
    },
}))
```

## Async and resilience

`async.Gather`/`Race`/`FirstSuccess` run a `Plan[T]` of labeled tasks under
an explicit `Limit` (`async.Limited(n)` or `async.Unlimited` — no implicit
"unbounded" default) and an explicit `Hooks` (`async.Hooks{}` for none):

```go
plan, err := async.NewPlan(async.Limited(4), async.Hooks{},
    async.Task[int]{Label: "a", Run: fetchA},
    async.Task[int]{Label: "b", Run: fetchB},
)
outcomes, err := async.Gather(ctx, plan) // source-index order, errors.Join'd
```

`Hooks.OnTaskComplete` reports permit-queue wait time separately from a
task's own run time — the split isn't visible from outside `Gather`/`Race`.

`async.Map`/`ForEach` run one function over a collection from a fixed pool
of `Limit` goroutines, rather than one goroutine per item, and return an
`Outcome` per item in input order. Run it inside a read to fan out over an
owned slice; every worker finishes before `Map` returns, so the borrow covers
them all:

```go
results, err := owner.View(func(items []Item) ([]async.Outcome[Result], error) {
    return async.Map(ctx, async.Limited(8), async.Hooks{}, items, process)
})
```

`async.Broadcast` fans one `*ownership.Owner[T]` out to concurrent workers
using `Owner[T].View`'s existing concurrent-read guarantee. `async.Pipeline`
chains heterogeneously-typed stages via a generic `Then[R any]` method.
`async.Group` wraps `sync.WaitGroup.Go` with panic recovery and a
context-aware `Close`.

`resilience.Retry` runs a function under a `Policy` (attempt limit, optional
error `Classifier`, `Backoff`, injectable `Clock`):

```go
backoff, err := resilience.ExponentialBackoff(100*time.Millisecond, 5*time.Second, 0.2)
value, err := resilience.Retry(ctx, resilience.Policy{
    MaxAttempts: 5,
    Backoff:     backoff,
}, fetch)
```

`resilience.Breaker` stops calling a resource that keeps failing and probes
it again after `OpenFor`. Rejection is immediate — nothing in a breaker
waits, and transitions happen lazily on the next call rather than on a timer.
`Do` is a generic method; `Allow` serves calls that cannot be wrapped:

```go
breaker, err := resilience.NewBreaker(resilience.BreakerPolicy{
    Trip:    resilience.FailureRatio(0.5, 20),
    OpenFor: 30 * time.Second,
    Failure: func(err error) bool { return !errors.Is(err, context.Canceled) },
})
value, err := breaker.Do(ctx, fetch)           // errors.Is(err, resilience.ErrOpen) when tripped
```

Compose it with `Retry` by nesting and classifying `ErrOpen` as not
retryable, so a tripped breaker ends the loop instead of burning attempts.

## Development

`just` is optional; every recipe maps to these commands:

```sh
gofmt -w .
go vet ./...
go test ./...
go test -race ./...
go test -tags=velocitydebug ./...
go test ./ownership -run '^$' -fuzz '^FuzzOwnershipModel$' -fuzztime 30s
go test ./... -run '^$' -bench . -benchmem
go -C benchmarks test ./... -run '^$' -bench . -benchmem -count=5
```

The nested `benchmarks` module holds head-to-head comparisons against the
libraries `dedupe` and `async` drew from — `resenje.org/singleflight`,
`go-singleflightx`, `x/sync`, and `hunch` — plus a `dedupe` backend
comparison. It is a separate module, so those do not run under
`go test ./...`; see [`benchmarks/README.md`](benchmarks/README.md) for how to
run them, what each arm does and does not do, and an indicative result table.
