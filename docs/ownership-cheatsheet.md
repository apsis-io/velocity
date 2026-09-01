# Ownership cheatsheet

Deterministic cleanup and handoff for Go values, with borrow checks as a
runtime assertion layer. Not compile-time ownership, not deep immutability.

Canonical package: `github.com/apsis-io/velocity/ownership`
Model and reasoning: [ownership.md](ownership.md) · Project guide: [README.md](../README.md)

## Pick the entry point

| shape | use |
|---|---|
| an `io.Closer` to own | `NewCloser(c)` |
| a plain value to hand off or borrow-check | `Own(v)` |
| a value with custom cleanup | `New(v, WithDrop(...))` |
| a read-only value to publish | `NewFrozen(v)` / `owner.Freeze()` |
| a permit, allocation, or reference to hand back | `NewLease(v, release)` |
| several resources acquired in sequence | `NewScope()` |
| a bounded set of reusable resources | `pool.New(...)` — checkouts are leases |
| counted handles with `Drop` on the last | `NewShared(v)` / `owner.IntoShared()` |

If a plain `defer Close()` would do, use that. Ownership earns its ceremony
only where a lifetime mistake leaks, closes early, uses after close, or makes
a goroutine handoff ambiguous.

## Lifecycle at a glance

```text
Own / New / NewCloser -> Owner[T]

Owner.Move()       -> Owner[T]    // old handle spent (ErrMoved)
Owner.Detach()     -> T           // leaves ownership; Drop never runs
Owner.Map(fn)      -> Owner[U]    // new type, Drop chain preserved
Owner.IntoShared() -> Shared[T]   // counted; Clone adds a handle
Owner.Freeze()     -> Frozen[T]   // counted, read-only by type

Shared.IntoOwner() / Frozen.IntoOwner() -> Owner[T]  // sole + unborrowed only

Seal()  -> no new borrows, irreversible
Drained() <-chan struct{}         // closed once sealed and borrow-free

final Release() or Close() -> Drop runs once
```

## Construction

```go
func Own[T any](value T) *Owner[T]                          // cannot fail
func New[T any](value T, opts ...Option[T]) (*Owner[T], error)
func NewCloser[T io.Closer](value T) *Owner[T]              // cannot fail
func NewShared[T any](value T, opts ...Option[T]) (*Shared[T], error)
func NewFrozen[T any](value T, opts ...Option[T]) (*Frozen[T], error)
func NewLease[T any](value T, release func(T) error) (*Lease[T], error)
func NewScope() *Scope

func WithDrop[T any](drop traits.Drop[T]) Option[T]     // func(T) error
func WithClone[T any](clone traits.Clone[T]) Option[T]  // func(T) (T, error); enables Snapshot
```

## Scoped access — the default

The borrow lasts exactly as long as the call. The value must not outlive it.

```go
func (o *Owner[T]) View[R any](fn func(T) (R, error)) (R, error)
func (o *Owner[T]) Mutate[R any](fn func(*T) (R, error)) (R, error)
func (o *Owner[T]) WithRead(fn func(T) error) error
func (o *Owner[T]) WithWrite(fn func(*T) error) error
// Shared has all four; Frozen has View and WithRead only.
```

```go
name, err := cfg.View(func(c Config) (string, error) { return c.Name, nil })
err = cfg.WithWrite(func(c *Config) error { c.Retries++; return nil })
```

Concurrent `View`s coexist. A `Mutate` during a `View`, or any access during
a `Mutate`, returns `ErrConflict` immediately — nothing waits. A returned
error does not roll a mutation back.

## Advanced borrows — explicit lifetime

For a borrow that must span a call boundary (a goroutine, a dedupe round).
Always release; a leaked borrow blocks its cell until it is released.

```go
func (o *Owner[T]) Borrow() (*ReadBorrow[T], error)
func (o *Owner[T]) BorrowMut() (*WriteBorrow[T], error)

func (b *ReadBorrow[T]) Project[R any](fn func(T) (R, error)) (R, error)
func (b *WriteBorrow[T]) Update[R any](fn func(*T) (R, error)) (R, error)
func (b *ReadBorrow[T]) Release() error   // idempotent; Close is an alias
```

Under `-tags=velocitydebug`, a borrow that becomes unreachable while still
held is logged through `slog.Default()` and released. Production builds have
no such net; that is deliberate, and why `Borrow` is cheap there. The static
net is `just lint`: the `lostrelease` analyzer reports a handle not released
on every path.

## Capabilities

```go
type Viewer[T any]  interface { WithRead(func(T) error) error }
type Mutator[T any] interface { Viewer[T]; WithWrite(func(*T) error) error }
func View[T, R any](v Viewer[T], fn func(T) (R, error)) (R, error)
func Mutate[T, R any](m Mutator[T], fn func(*T) (R, error)) (R, error)
```

Owner and Shared are Mutators; Frozen is a Viewer only. Ask for the
capability you need in the signature.

## Transfer

```go
func (o *Owner[T]) Move() (*Owner[T], error)
func (o *Owner[T]) Detach() (T, error)
func (o *Owner[T]) Map[U any](fn func(T) (U, error), opts ...Option[U]) (*Owner[U], error)
func (o *Owner[T]) IntoShared() (*Shared[T], error)
func (o *Owner[T]) Freeze() (*Frozen[T], error)
func (s *Shared[T]) Clone() (*Shared[T], error)
func (s *Shared[T]) IntoOwner() (*Owner[T], error)
func (f *Frozen[T]) Clone() (*Frozen[T], error)
func (f *Frozen[T]) IntoOwner() (*Owner[T], error)
```

Every transfer requires no outstanding borrows and fails with `ErrConflict`
otherwise. `Detach` is the only exit that skips `Drop`; reaching for it to
pass a bare `T` through an API is how resources leak. `Map`'s callback must
not close the source value — the source `Drop` still runs, after the derived
one.

## Retirement

```go
func (o *Owner[T]) Seal() error
func (o *Owner[T]) Drained() <-chan struct{}
```

```go
owner.Seal()
select {
case <-owner.Drained():
case <-ctx.Done():
    return ctx.Err()
}
return owner.Release()
```

The wait is yours: nothing in the package blocks, which is what makes it
unable to deadlock.

## Lease and Scope

```go
func (l *Lease[T]) Value() (T, error)   // ErrReleased once handed back
func (l *Lease[T]) Held() bool
func (l *Lease[T]) Move() (*Lease[T], error)
func (l *Lease[T]) Release() error      // exactly once; repeats return the first error

func (s *Scope) Own[T any](owner *Owner[T]) error   // moves the owner in
func (s *Scope) OwnCloser(closer io.Closer) error
func (s *Scope) OnRelease(release func() error) error
func (s *Scope) Disarm() int            // success path: the built value owns them now
func (s *Scope) Close() error           // LIFO, continues past failures, errors joined
```

```go
scope := ownership.NewScope()
defer scope.Close()
conn, err := dial(); if err != nil { return nil, err }
_ = scope.OwnCloser(conn)
raw, err := dial(); if err != nil { return nil, err }   // scope.Close closes conn
_ = scope.OwnCloser(raw)
scope.Disarm()
return &Bundle{conn, raw}, nil
```

## State and errors

```go
type State struct {
    Shared, Frozen, Sealed, Released, Moved bool
    Readers, Shares int
    Writer    bool
    DropError error
}
func (o *Owner[T]) State() State   // also on Shared and Frozen
```

Sentinels: `ErrConflict`, `ErrMoved`, `ErrReleased`, `ErrSealed`,
`ErrNoClone`, `ErrProjection`, `ErrInvalidConfig`, `ErrDuplicateOption`,
`ErrNilOption`, `ErrScopeClosed`. Typed wrappers (`ConflictError`,
`MovedError`, `ReleasedError`, `SealedError`, `ScopeError`, `ConfigError`,
…) carry the `Operation` and unwrap to the sentinel.

## Contract

- Callbacks return normally. A panic in a scoped callback still releases the
  borrow; everything else about it is ordinary Go.
- `Drop` runs at most once, on final release, outside the lock; its first
  error is returned once and retained in `State.DropError`.
- Assigning a `*Shared` or `*Frozen` does not count a handle. Call `Clone`.
- Aliases are invisible: a slice, map, pointer, or interface reached through a
  callback remains usable after the borrow ends. Use `Snapshot` with a real
  `Clone` when isolation matters.
