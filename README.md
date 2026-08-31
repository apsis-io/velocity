# velocity

Experimental Go 1.27 concurrency foundations. The current release contains
composable clone/drop traits and runtime ownership/borrow-state enforcement.

## Scoped access

```go
owner, err := ownership.New([]int{1, 2, 3})
if err != nil {
    return err
}
defer owner.Release()

sum, err := owner.Read(func(access ownership.ReadAccess[[]int]) (int, error) {
    return access.Project(func(values []int) (int, error) {
        return values[0] + values[1] + values[2], nil
    })
})
```

Mutation uses an exclusive capability. A returned error does not roll changes
back:

```go
_, err = owner.Write(func(access ownership.WriteAccess[[]int]) (struct{}, error) {
    return access.Update(func(values *[]int) (struct{}, error) {
        *values = append(*values, 4)
        return struct{}{}, nil
    })
})
```

## Advanced borrows

Advanced handles make lifetime explicit. Always release them:

```go
borrow, err := owner.Borrow()
if err != nil {
    return err
}
defer borrow.Release()

first, err := borrow.Project(func(values []int) (int, error) {
    return values[0], nil
})
```

Multiple reads may coexist. A write borrow, move, take, or release during a read
returns `ownership.ErrConflict` immediately; ownership never waits internally.
Sharing one write capability between goroutines is safe, but overlapping updates
also return `ErrConflict` rather than receiving the same mutable pointer.

## Move, take, and sharing

```go
moved, err := owner.Move()       // old owner becomes moved
value, err := moved.IntoValue()       // exits ownership without running Drop

owner, _ = ownership.New(value)
shared, err := owner.IntoShared()
peer, err := shared.Clone()      // explicit counted handle
_ = peer.Release()
owner, err = shared.IntoOwner()  // succeeds only when sole and unborrowed
```

`Release` and `Close` are exact aliases. Cleanup is idempotent after successful
release or transfer. A successful borrow release has completed its state change;
it is never silently deferred. Reentrant release from Drop returns immediately.

## Drop and snapshot

```go
owner, err := ownership.New(
    []byte("velocity"),
    ownership.WithDrop(func(value []byte) error {
        clear(value)
        return nil
    }),
    ownership.WithClone(func(value []byte) ([]byte, error) {
        return bytes.Clone(value), nil
    }),
)
copy, err := owner.Snapshot()
```

Drop runs at most once on explicit final release. Its first error is returned
once and retained by `State`. Runtime cleanup never runs Drop. A Clone is only
as independent as its implementation; velocity cannot validate clone quality.

## Resource patterns

`NewCloser` owns an `io.Closer`, and `View`/`Mutate` read and write without
the intermediate accessor:

```go
conn := ownership.NewCloser(rawConn)
defer conn.Close()

name, err := cfg.View(func(c Config) (string, error) { return c.Name, nil })
err = cfg.WithWrite(func(c *Config) error { c.Retries++; return nil })
```

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
intact — unlike `IntoValue`, which exits ownership without running Drop:

```go
writer, err := file.Map(
    func(f *os.File) (*bufio.Writer, error) { return bufio.NewWriter(f), nil },
    ownership.WithDrop(func(w *bufio.Writer) error { return w.Flush() }),
)
// Releasing writer flushes it, then closes the file underneath.
```

The callback must not close the source value itself: the source Drop still
runs afterwards.

## Safety boundary

This package enforces borrow state at runtime. It is not Rust ownership and does
not add deep `const` to Go. A map, slice, pointer, interface, or projected value
can retain aliases outside a callback. Use a correct Clone and `Snapshot` when
isolation matters. See [`docs/ownership.md`](docs/ownership.md).

All user callbacks, Clone, and Drop functions must return normally. They must
not panic or call `runtime.Goexit`; returned errors are the supported failure
channel. Deferred scoped cleanup still releases borrows if a violating callback
panics.

With `-tags=velocitydebug`, leaked advanced borrows emit structured diagnostics
through `slog.Default()`. Applications may configure any handler, including
`tint`; velocity does not configure global logging.

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

`async.Broadcast` fans one `*ownership.Owner[T]` out to concurrent workers
using `Owner[T].Read`'s existing concurrent-read guarantee. `async.Pipeline`
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
