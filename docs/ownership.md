# Ownership model

`ownership` enforces a small runtime state machine around values that remain
inside its handles. It makes conflicting concurrent access observable and
transfer explicit. It cannot provide compile-time alias analysis or deep
immutability.

## Cell states

```text
             IntoShared
 unique --------------------> shared
   |  \                         |  \
   |   \ Move -> unique        |   \ Clone -> shared (+1 handle)
   |                            |    \
   | IntoValue                       |     \ IntoOwner (sole, unborrowed)
   |                            |      ----------------------------> unique
   v                            v
 released <---------------- final explicit Release
```

A unique cell has one active Owner and no Shared handles. A shared cell has no
Owner and one or more explicitly cloned Shared handles. A released cell has no
value, readers, writer, Owner, or Shared handle.

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

## Future dedupe integration

A future borrowed-dedupe API may take a read or write lease from a leader-owned
Owner before registering work, hold the loan for the generation while any
caller remains interested, release it before publishing the computed result,
and leave duplicate callers' unused inputs untouched. Ordinary result-only
dedupe remains independent of ownership.
