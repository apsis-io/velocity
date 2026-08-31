# Ownership cheatsheet

Runtime lifecycle and borrow-state enforcement for Go values—useful with [[ownership]], [[Go ownership]], [[Go generics]], [[Structured concurrency]], and [[Resource cleanup]], but not compile-time or deep ownership.

Canonical package: `github.com/apsis-io/velocity/ownership`  
Detailed model: [[ownership design]] / [ownership.md](ownership.md)  
Project guide: [README.md](../README.md)

## Lifecycle at a glance

```text
New(value) -> Owner[T]

Owner.Move()       -> Owner[T]  // old handle becomes moved
Owner.IntoValue()  -> T         // leaves ownership; Drop does not run
Owner.IntoShared() -> Shared[T]

Shared.Clone()     -> Shared[T] // explicit counted handle
Shared.IntoOwner() -> Owner[T]  // only sole + unborrowed

final Owner/Shared Release() or Close() -> Drop once
```

## Construction and traits

```go
type Option[T any] interface { /* sealed */ }

func New[T any](value T, opts ...Option[T]) (*Owner[T], error)
func WithDrop[T any](drop traits.Drop[T]) Option[T]
func WithClone[T any](clone traits.Clone[T]) Option[T]

// github.com/apsis-io/velocity/traits
type Drop[T any] func(T) error
type Clone[T any] func(T) (T, error)
```

```go
owner, err := ownership.New(
    value,
    ownership.WithDrop(func(value Resource) error {
        return value.Close()
    }),
    ownership.WithClone(func(value Resource) (Resource, error) {
        return value.Clone()
    }),
)
if err != nil {
    return err
}
defer owner.Release()
```

## Exact handle API

```go
type Owner[T any] struct { /* unexported; do not copy */ }

func (o *Owner[T]) Read[R any](func(ReadAccess[T]) (R, error)) (R, error)
func (o *Owner[T]) Write[R any](func(WriteAccess[T]) (R, error)) (R, error)
func (o *Owner[T]) Borrow() (*ReadBorrow[T], error)
func (o *Owner[T]) BorrowMut() (*WriteBorrow[T], error)
func (o *Owner[T]) Move() (*Owner[T], error)
func (o *Owner[T]) IntoValue() (T, error)
func (o *Owner[T]) IntoShared() (*Shared[T], error)
func (o *Owner[T]) Snapshot() (T, error)
func (o *Owner[T]) State() State
func (o *Owner[T]) Release() error
func (o *Owner[T]) Close() error
```

```go
type Shared[T any] struct { /* unexported; do not copy */ }

func (s *Shared[T]) Clone() (*Shared[T], error)
func (s *Shared[T]) Read[R any](func(ReadAccess[T]) (R, error)) (R, error)
func (s *Shared[T]) Write[R any](func(WriteAccess[T]) (R, error)) (R, error)
func (s *Shared[T]) Borrow() (*ReadBorrow[T], error)
func (s *Shared[T]) BorrowMut() (*WriteBorrow[T], error)
func (s *Shared[T]) IntoOwner() (*Owner[T], error)
func (s *Shared[T]) Snapshot() (T, error)
func (s *Shared[T]) State() State
func (s *Shared[T]) Release() error
func (s *Shared[T]) Close() error
```

`Close` is an exact alias for `Release`. Assigning a `*Shared[T]` pointer does not create another counted handle; call `Clone`.

## Scoped access—the default

```go
type ReadAccess[T any] struct { /* scoped */ }
func (a ReadAccess[T]) Project[R any](func(T) (R, error)) (R, error)

type WriteAccess[T any] struct { /* scoped */ }
func (a WriteAccess[T]) Update[R any](func(*T) (R, error)) (R, error)
```

```go
name, err := owner.Read(func(access ownership.ReadAccess[Config]) (string, error) {
    return access.Project(func(cfg Config) (string, error) {
        return cfg.Name, nil
    })
})

_, err = owner.Write(func(access ownership.WriteAccess[Config]) (struct{}, error) {
    return access.Update(func(cfg *Config) (struct{}, error) {
        cfg.Enabled = true
        return struct{}{}, nil
    })
})
```

The capability expires when the outer `Read`/`Write` callback returns. An access callback already running in another goroutine may finish; new calls through the escaped capability return `ErrReleased`, and the borrow is released when the final active callback ends.

## Advanced explicit borrows

```go
type ReadBorrow[T any] struct { /* unexported; do not copy */ }
func (b *ReadBorrow[T]) Project[R any](func(T) (R, error)) (R, error)
func (b *ReadBorrow[T]) Release() error
func (b *ReadBorrow[T]) Close() error

type WriteBorrow[T any] struct { /* unexported; do not copy */ }
func (b *WriteBorrow[T]) Update[R any](func(*T) (R, error)) (R, error)
func (b *WriteBorrow[T]) Release() error
func (b *WriteBorrow[T]) Close() error
```

```go
read, err := owner.Borrow()
if err != nil {
    return err
}
defer read.Release()
value, err := read.Project(func(value T) (T, error) { return value, nil })

write, err := owner.BorrowMut()
if err != nil {
    return err
}
defer write.Release()
_, err = write.Update(func(value *T) (struct{}, error) {
    // mutate value
    return struct{}{}, nil
})
```

Advanced Release is linearizable. During an active Project/Update it returns `ErrConflict`; retry after the callback ends. Nil means released or already released—never deferred.

## Sharing and conversion

```go
shared, err := owner.IntoShared()
if err != nil {
    return err
}
defer shared.Release()

peer, err := shared.Clone()
if err != nil {
    return err
}
if err := peer.Release(); err != nil {
    return err
}

owner, err = shared.IntoOwner() // succeeds only when sole + unborrowed
```

A failed `IntoOwner` leaves the Shared handle unchanged.

## Snapshot

```go
owner, err := ownership.New(
    []byte("secret"),
    ownership.WithClone(func(value []byte) ([]byte, error) {
        return bytes.Clone(value), nil
    }),
)
snapshot, err := owner.Snapshot()
```

Snapshot validates handle liveness first, temporarily read-borrows, then calls Clone outside lifecycle locks. Without Clone it returns `ErrNoClone`.

## State

```go
type State struct {
    Shared    bool
    Released  bool
    Moved     bool
    Readers   int
    Writer    bool
    Shares    int
    DropError error
}
```

`State` is synchronized and never contains the owned value.

## Errors

```go
var (
    ErrConflict        error
    ErrMoved           error
    ErrReleased        error
    ErrNoClone         error
    ErrInvalidConfig   error
    ErrProjection      error
    ErrDuplicateOption error
    ErrNilOption       error
)

type ConflictError struct {
    Operation Operation
    Readers   int
    Writer    bool
    Shares    int
}
type MovedError struct{ Operation Operation }
type ReleasedError struct{ Operation Operation }
type NoCloneError struct{ Operation Operation }
type ProjectionError struct{ Operation Operation }
type ConfigError struct {
    Option string
    Reason error
}
```

```go
if errors.Is(err, ownership.ErrConflict) {
    var conflict *ownership.ConflictError
    if errors.As(err, &conflict) {
        log.Printf("%s: readers=%d writer=%t shares=%d",
            conflict.Operation, conflict.Readers, conflict.Writer, conflict.Shares)
    }
}
```

```go
type Operation string

const (
    OpBorrow      Operation = "borrow"
    OpBorrowMut   Operation = "borrow mutable"
    OpMove        Operation = "move"
    OpIntoValue   Operation = "into value"
    OpIntoShared  Operation = "into shared"
    OpClone       Operation = "clone shared"
    OpIntoOwner   Operation = "into owner"
    OpRelease     Operation = "release"
    OpProject     Operation = "project"
    OpUpdate      Operation = "update"
    OpSnapshot    Operation = "snapshot"
)
```

## Concurrency guarantees

- Multiple read borrows and projections may coexist.
- One write borrow excludes all readers and writers.
- Overlapping Update calls through the same write capability return `ErrConflict`.
- Borrow conflicts fail immediately; ownership never waits for access.
- Successful Release completes its state transition before returning.
- Final release marks the cell released and detaches the value before Drop.
- Only the first final releaser runs Drop; reentrant/concurrent later Release calls return nil without waiting.
- Drop errors go to the first releaser and then remain visible in `State.DropError`.
- Project, Update, Clone, and Drop callbacks must return normally—not panic or call `runtime.Goexit`.

## What ownership cannot enforce

- Pre-existing aliases created before `New`.
- Maps, slices, pointers, interfaces, channels, or interior pointers retained by callbacks.
- Deep immutability or Go `const` semantics.
- Transactional rollback when Update returns an error.
- Whether Clone really returns independent storage.
- Compile-time moves, lifetime analysis, or Rust-equivalent ownership.

See [[Go ownership]], [[Resource cleanup]], and the detailed [ownership design](ownership.md) before relying on the safety boundary.
