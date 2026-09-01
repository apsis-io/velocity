# Ownership model

`ownership` enforces a small runtime state machine around values that remain
inside its handles. It makes conflicting concurrent access observable and
transfer explicit. It cannot provide compile-time alias analysis or deep
immutability.

## Cell states

```text
                    IntoShared
        .--------------------------------> shared
        |                                    | \ Clone -> shared (+1 handle)
        |         <-- IntoOwner (sole, unborrowed) --
        |
 unique -+-- Move -> unique
        |-- IntoValue -> bare T (no Drop)
        |-- Map -> unique (new cell over U; this cell released)
        |
        |             Freeze
        '--------------------------------> frozen
                                             | \ Clone -> frozen (+1 handle)
                  <-- IntoOwner (sole, unborrowed) --

 any mode --- final explicit Release ---> released
```

A unique cell has one active Owner and no counted handles. A shared cell has no
Owner and one or more explicitly cloned Shared handles. A frozen cell is a
shared cell that can never take a write borrow: `Frozen[T]` exposes no write
operation, so read-only is enforced by the type rather than rejected at
runtime. A released cell has no value, readers, writer, or handle of any kind.

`Map` is the only transition that produces a cell over a different type. It
releases the source cell and hands the source Drop to the derived cell, which
runs the derived Drop first and the source Drop second, against the retained
source value. The callback must not release the source value itself.

Each public handle is separately active, moved, or released. Pointer aliases of
a handle observe its transition together; assigning a `*Shared[T]` does not
increment the handle count. Call `Clone` explicitly.

## Borrow invariants

- Zero or more read borrows coexist, including concurrent projections through
  the same read-borrow handle.
- One write borrow excludes every reader and writer. Concurrent updates through
  the same write capability also conflict rather than sharing `*T`.
- Acquisition, access, move, take, share, unwrap, and release conflicts fail
  immediately with a typed error. Nothing waits internally.
- A handle cannot release while it owns an advanced borrow. A non-final Shared
  handle can release while another handle owns a read; final release cannot.
- User code never runs while the cell mutex is held. Compatible nested reads
  work; conflicting reentrant operations fail instead of deadlocking.
- Callback accessors expire before the callback returns. Advanced handles
  validate liveness on every Project/Update.

## Transfer and release

`Move` returns a fresh Owner over the same cell and invalidates the old handle.
`IntoValue` consumes the Owner and returns bare T without invoking Drop.
`IntoShared` consumes unique ownership. `IntoOwner` is the inverse only when its
Shared handle is the sole handle and there are no borrows.

Release and Close are exact aliases and linearizable: nil means the borrow or
handle was released (or already released), never that release was deferred.
Final release commits the cell to released, detaches and zeroes its value, and
unlocks before invoking Drop. Drop runs at most once. Concurrent or reentrant
later releases return nil immediately instead of waiting for Drop. Its error is
returned by the first release only, retained in State after Drop returns, and
not retried.

`runtime.AddCleanup` protects only leaked advanced borrow leases and emits a
leak diagnostic under `velocitydebug`. It never decrements Owner/Shared handle
counts, invokes Drop, or provides deterministic correctness.

That protection is most of the cost of an advanced borrow: it accounts for four
of the five allocations `Borrow` makes. `BorrowUntracked` and
`BorrowMutUntracked` skip it, and are otherwise identical in preconditions,
conflicts, and returned type. They suit callers whose release is guaranteed on
every path including panics — `dedupe`'s borrowed-input API is one — and are
wrong anywhere release is merely intended, because a handle that is never
released then blocks its cell permanently. Scoped `Read`/`Write` remain the
default: they cannot leak and allocate once.

## Retirement

`Release` refuses an active borrow and returns immediately. That is correct for
the ordinary case but leaves no way to retire a value other goroutines are
still reading: stop admitting new readers, let the in-flight ones finish, then
close.

`Seal` supplies the half a caller cannot build, since only the cell knows the
borrow count and only it can turn a new `Borrow` away. `Drained` reports when
that has taken effect. The waiting belongs to the caller:

```go
owner.Seal()
select {
case <-owner.Drained():
case <-ctx.Done():
    return ctx.Err()
}
return owner.Release()
```

There is deliberately no blocking `Retire(ctx)`. Nothing in this package waits
internally, which is what makes deadlock impossible within it; an operation
that waited for borrows would hang a goroutine that holds one and then retires
the same value, where today that returns `ErrConflict` at once. Splitting the
operation keeps the invariant and moves that mistake into the caller's own
`select`, where it is visible.

Sealing applies to the value rather than to one handle, is irreversible and
idempotent, and releases nothing by itself; outstanding borrows and handles
must still be released. `Drained` closes only after sealing, because an
unsealed borrow count of zero is transient and a closed channel is not.
Abandoning the wait leaves the value sealed, so a later attempt can finish.

## When not to use this

Ownership costs ceremony. It earns that cost only where a lifetime mistake
would actually hurt. The test:

> Would an ownership violation here cause a leak, a premature close, a
> use-after-close, or an ambiguous handoff across goroutines?

If not, a plain `defer` is the better tool. Specifically, do not wrap:

- local files or sockets already covered by an obvious `defer Close()`;
- maps and queues that a mutex already guards;
- long-lived worker lifecycle joins, which are a `WaitGroup` problem;
- durable reconciliation state machines;
- values whose APIs demand unrestricted raw aliases, since the borrow rules
  cannot hold and will only produce conflicts;
- every semaphore and `WaitGroup`.

The shapes that do pay off are handoff across a goroutine boundary,
multi-step construction that must unwind on partial failure (`Scope`),
resources held and returned (`Lease`), and values published for concurrent
readers (`Frozen`).

## Choosing an entry point

| shape | use |
|---|---|
| an `io.Closer` to own | `NewCloser` |
| a read-only value to publish | `NewFrozen` |
| a permit, allocation, or reference to hand back | `NewLease` |
| a bounded set of reusable resources | `pool.New` (checkouts are leases) |
| several resources acquired in sequence | `NewScope` |
| anything else needing borrow enforcement | `New` |

`View`/`Mutate` read and write without the intermediate accessor;
`WithRead`/`WithWrite` are their error-only forms. All four keep the
callback-scoped lifetime: the value must not outlive the call.

`Detach` and `IntoValue` are the same operation. `Detach` is the name to
reach for, because it says what changes — the caller now owns cleanup, and
Drop will never run. Reaching for either merely to pass a value through an
API that wants a bare `T` is how resources leak.

## Alias boundary

Go assignment is shallow for slices, maps, pointers, channels, functions, and
interfaces. `Project` receives T by assignment and `Update` receives a pointer
to the cell value during an exclusive borrow. A callback can deliberately copy
or retain an interior alias; the library cannot revoke it later. Pre-existing
aliases from before `New` are equally invisible.

A configured Clone enables `Snapshot`, but correctness depends on that callback
producing a genuinely independent value. Identity/shallow clones do not create
immutability. Do not describe this package as Rust-equivalent ownership or
`const`; it enforces only transitions and borrow state visible through its API.

## Callback contract

Project, Update, Clone, and Drop callbacks must return normally and report
failure through errors. They must not panic or call `runtime.Goexit`. Scoped
borrows still use deferred release so a panic does not strand the borrow state,
but panic behavior is otherwise normal Go behavior and not part of the result
contract.

## Dedupe integration

`dedupe.Group.DoBorrowed` and `DoBorrowedMut` take a read or write lease from an
Owner before registering work, hold the leader's loan for the generation, and
release it before publishing the computed result. A duplicate input is briefly
borrowed only while determining whether its call is the leader, then released
without projection or mutation. Two concurrent mutable calls using the same
Owner may therefore conflict before role determination, as required by the
exclusive-borrow rule. If a caller's context is canceled while non-cooperative
work continues, the method may return before its leader loan is released; the
input is reusable after the callback completes, and `dedupe.Hooks.OnComplete`
signals after that release. Ordinary result-only `Do` remains independent of
an input Owner while returning its result as `Shared[V]`.
