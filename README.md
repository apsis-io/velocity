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

## Move, take, and sharing

```go
moved, err := owner.Move()       // old owner becomes moved
value, err := moved.Take()       // exits ownership without running Drop

owner, _ = ownership.New(value)
shared, err := owner.IntoShared()
peer, err := shared.Clone()      // explicit counted handle
_ = peer.Release()
owner, err = shared.TryUnwrap()  // succeeds only when sole and unborrowed
```

`Release` and `Close` are exact aliases. Cleanup is idempotent after successful
release or transfer.

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
```

The nested `benchmarks` module pins future comparison dependencies. It contains
no benchmark source or machine-specific result report yet.
