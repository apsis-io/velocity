# Current design decisions

This records current selections, not superseded planning alternatives.

## Repository and traits

- Module: `github.com/apsis-io/velocity`, Go 1.27 only, MIT license held by
  Malformed C, development `Version = "dev"`.
- No retained or updated upstream clones, hosted CI, external linter,
  GoReleaser, reflection registry, inheritance framework, or generated API
  unless repetition later proves generation useful.
- Traits are generic function types, not interfaces. The initial traits are
  Drop and Clone with strict nil validation, ordered Drop error joining,
  sequential Clone short-circuiting, and explicit intermediate cleanup.

## Trait composition (implemented)

- `traits.Drop[T]` gained a `Clone(clones ...Clone[T]) (Clone[T], error)`
  method, replacing the free function `ComposeClonesWithDrop` (pre-v1
  breaking rename, same precedent as ownership's `Take`→`IntoValue`). Call
  form: `traits.Drop[T](dropFn).Clone(clone1, clone2)` — `Drop[T](dropFn)` is
  a type conversion that reads like a constructor call, so no new exported
  type or naming collision was needed.
- Evaluated and rejected backing `traits` with an external functional library
  (`github.com/enetx/g`, `github.com/fogfish/golem`) instead of the current
  hand-rolled loop: `traits` imports nothing but stdlib `errors` today, and
  neither library's composition primitive naturally expresses "drop the
  superseded intermediate, never the caller's input or final result" —
  `g.Result[T].ThenOf` is the closer fit but still needs manual closures for
  the drop side effect, and golem's `duct` (AST/visitor, no ready executor)
  and `semigroup`/`monoid` (pure, error-free binary reduction) are structural
  mismatches. Decided without building the comparison benchmark; the
  20-line manual implementation isn't repeated enough elsewhere yet to
  justify a dependency, per the no-retained-upstream-clones stance above.

## Ownership (implemented)

- Concrete constructor-created Owner/Shared/access/borrow types use one
  concurrency-safe runtime state machine. No operation blocks for a borrow.
- Scoped Read/Write is the default vocabulary; Borrow/BorrowMut is the advanced
  explicit-handle vocabulary. Go 1.27 generic methods provide typed projection
  and update on concrete types and are not interface seams.
- Move creates a fresh Owner; IntoValue exits the ownership system; IntoShared and
  sole-handle IntoOwner provide explicit conversion. Shared cloning is explicit.
- Release and Close are aliases. Optional Drop is explicit, at most once, and
  never driven by GC. Optional Clone powers Snapshot.
- Normal callbacks, Clone, Drop, and future Observer callbacks must not panic or
  call Goexit. Errors and context causes are the supported failure paths.
- This is runtime borrow-state enforcement, not compile-time ownership, deep
  immutability, rollback, or alias revocation.
- `Frozen[T]` is the one exception to "runtime enforcement": it exposes no
  write operation at all, so read-only is a property of the type rather than
  a conflict rejected at runtime. A flag on `Shared` could not provide that,
  which is why it is a distinct type rather than a mode bit. It is
  reference-counted like `Shared`, and `IntoOwner` thaws the sole unborrowed
  handle back to a mutable `Owner`.
- `Owner.Map[U]` is the only transition producing a cell over a different
  type, and exists because `IntoValue` — previously the only exit — does not
  run Drop, so wrapping an owned resource silently discarded its cleanup.
  The derived cell's Drop runs the derived policy first, then the source
  Drop against the retained source value: unwrap before closing what was
  wrapped. Caller obligation, documented at the method: `fn` must not
  release the source value, since the source Drop still runs later. `Map`
  holds an exclusive lease across `fn` and commits the transfer in the same
  critical section that releases it, so no borrow can interleave after `fn`
  has already produced the derived value; a failed `fn` leaves the source
  `Owner` untouched.
- The package's weak point was friction rather than enforcement: callers
  already have `defer`, `Close`, and scoped cleanup, so wrapping a value in
  `Owner[T]` could cost more than the problem it solved. `View`/`Mutate` and
  their error-only `WithRead`/`WithWrite` forms drop the intermediate
  accessor; `NewCloser` and `NewFrozen` give the two common shapes a direct
  entry point. All of them adapt the existing paths, so callback-scoped
  lifetimes are unchanged.
- `Scope` models transactional acquisition — reverse-order release, continue
  past failures, joined errors, idempotent `Close`, explicit `Disarm` for the
  success path. Enrolment after `Close` or `Disarm` is rejected rather than
  silently dropped, so a late resource stays the caller's responsibility.
  There is no GC-driven cleanup: an unclosed `Scope` is indistinguishable
  from one whose resources were deliberately transferred.
- `Lease[T]` is deliberately *not* borrow-checked, which is why it is a
  separate type rather than a rename of `Owner`. Its resources are copyable
  identifiers — permits, IP allocations, UID bases, unit references — where
  excluding concurrent readers protects nothing, so `Value` returns a checked
  copy directly instead of routing through a callback. It enforces
  release-exactly-once and rejects use-after-release, and documents that it
  offers no aliasing protection; `Owner` stays the answer when that matters.
  Release callbacks take no context by design: they should be bounded, and
  cancellation would be added only for a resource whose semantics define it.
- `Seal`/`Drained` cover graceful retirement — refuse new borrows, then wait
  for the in-flight ones — without a blocking operation. A `Retire(ctx)` was
  proposed and rejected: this package has no channels, selects, or context
  waits anywhere, and that absence is why it cannot deadlock. A waiting
  retire would hang a goroutine that holds a borrow and then retires the same
  value, where `Release` returns `ErrConflict` immediately today, and would
  also make Drop timing non-deterministic (a retire could time out and then
  succeed later, with the Drop error going nowhere but `State`). Splitting it
  gives the caller the one primitive it cannot build — only the cell can
  refuse a `Borrow` — while the waiting stays in the caller's own `select`,
  composed with whatever shutdown context it already has.
- `Drained` closes only once sealed, since an unsealed borrow count of zero
  is transient and closing a channel is not. Sealing is a property of the
  value, not of a handle, and is irreversible.
- A `Scope.CloseContext(ctx)` was proposed and rejected for the same reason,
  plus one specific to cleanup. `Scope` holds `func() error` thunks, and the
  common one is `io.Closer.Close`, which takes no context and cannot be
  interrupted once entered. A context could therefore only decide whether to
  *start* the remaining releases, never bound any single one — so the name
  promises "cleanup with a timeout" and delivers "cleanup that may partly not
  happen." Abandoning the rest on expiry leaks them permanently, since `Close`
  has already taken the release list and nothing can retry; running them in a
  goroutine instead leaves work outstanding in a shutdown path, where the
  caller is usually about to exit, and discards the errors of anything that
  finishes late. Taken with the rule that release callbacks stay bounded, it
  is redundant when the rule holds and ineffective when it does not.
- The genuine case — a resource whose release really is cancellable with
  defined semantics — needs no API at all: capture the context in the thunk,
  `scope.OnRelease(func() error { return conn.Shutdown(ctx) })`. That scopes
  cancellation to the one resource that defines it, instead of claiming a
  scope-wide guarantee the scope cannot keep.
- `Detach` is an exact alias of `IntoValue`, added because the name
  `IntoValue` describes the return value rather than the consequence, and the
  consequence — Drop will never run — is what callers get wrong when they
  reach for it merely to pass a value through an API.
- `docs/ownership.md` now states where ownership does *not* belong (values
  already covered by an obvious `defer`, mutex-guarded collections, worker
  lifecycle joins, APIs demanding raw aliases). The intended test is whether
  a violation would cause a leak, premature close, use-after-close, or
  ambiguous cross-goroutine handoff; if not, this package adds ceremony
  rather than safety.
- `Borrow{,Mut}Untracked` skip the `runtime.AddCleanup` leak net, which is
  four of the five allocations a tracked advanced borrow makes (379 ns ->
  161 ns, 5 -> 2 allocs). They are for callers whose release is guaranteed
  on every path including panics, and are wrong where release is merely
  intended: an unreleased handle then blocks its cell permanently instead of
  being reclaimed. `dedupe`'s borrowed-input API is the demonstrated caller.
  Scoped `Read`/`Write` stay the default — they cannot leak and allocate
  once.

## Ownership repositioned (implemented)

A design review concluded that `ownership`'s resource-pattern layer was good
and its generic runtime-borrow-checker core promised more than Go can
deliver: borrow enforcement is exact for value types, which rarely need it,
and porous for reference types, where a projected alias can never be
revoked; and `ErrConflict`-on-contention is the wrong default for shared
state, where waiting (`RWMutex`) is what callers want. The package is now
positioned as **deterministic cleanup and handoff, with borrow checks as an
assertion layer**, and the API was cut to match:

- `ReadAccess`/`WriteAccess` and `Read`/`Write` are gone. `View`/`Mutate`
  and `WithRead`/`WithWrite` were already the ergonomic form and the older
  layer only added a second level of ceremony for the same lifetime. Scoped
  access now holds the lease directly through one shared helper per
  direction; a scoped read dropped from ~141 ns to ~100 ns because two
  closures went with it.
- `IntoValue` is gone; `Detach` is the name, because it says what changes.
- `BorrowUntracked`/`BorrowMutUntracked` are gone, and so is the
  unconditional `runtime.AddCleanup` on advanced borrows. In production a
  leaked borrow blocks its cell until released — a deterministic, visible
  failure — rather than being silently reclaimed at whatever moment the GC
  chooses, which turned a bug into a heisenbug and cost four of five
  allocations. Under `-tags=velocitydebug` the cleanup still registers,
  logs the leak, and releases. `Borrow` is now ~164 ns / 2 allocs from
  ~385 ns / 5.
- `Shared` stays. Hand-counted handles in a GC'd language exist for exactly
  one thing, running `Drop` deterministically on the last release, and
  that is also the hook a future manual-free runtime proposal would need;
  a `Drop` that can return memory is only useful if something knows when
  the last user is gone.
- The no-wait invariant is untouched. It is the design.

Three follow-ups from the same review:

- **Scoped access allocates nothing.** A lease exists to give an advanced
  borrow an identity to release later; a scoped call releases right here,
  so `View`/`Mutate` now bump the cell's counters directly through the same
  `admitReadLocked`/`admitWriteLocked` checks advanced borrows use, and undo
  them in a defer. 100 ns / 1 alloc → 47 ns / 0 allocs, roughly 2x a bare
  `RWMutex`.
- **Capability interfaces.** `Viewer[T]`/`Mutator[T]` put read-only intent
  in a signature, checked by the compiler, which extends the one type-level
  guarantee `Frozen` had to every call boundary. Go 1.27 still rejects
  generic methods in interfaces, so they name `WithRead`/`WithWrite` and
  package-level `View`/`Mutate` restore the `(R, error)` shape.
- **The leak check is static.** `analysis/lostrelease` is a `go/analysis`
  pass modelled on vet's `lostcancel`: a `Borrow`/`BorrowMut`/`NewLease`/
  `pool.Get` handle assigned to `_` or not used on every path to a return
  is reported. "Use" is conservative — any mention discharges it except
  `Project`/`Update`/`Value`/`Held`/`State` and `_ = h` — and the failure
  branch of the acquisition's own `err` check is not a path. A blank handle
  in an `if` init whose condition inspects `err` is a probe of whether
  acquisition fails, not a discard, which is how tests assert conflicts. It
  lives in its own module so the library does not depend on `x/tools`, and
  runs through `go vet -vettool` (`just lint`). Running it over velocity
  itself found only test code releasing inside `for range 3` loops, which
  the CFG treats as possibly zero-iteration; the analyzer now prunes the
  exit edge of a loop that provably runs at least once — `for {}`, a
  constant range or count, a non-empty literal or array — and still assumes
  a runtime-bounded loop may not run.
- **A concurrent model test** now drives one cell from eight goroutines
  through every access and transfer operation with a deadline, so the
  no-wait claim is exercised rather than asserted.

Two result-shape changes followed the comparison benchmarks, both breaking
and both made because velocity had no consumers yet:

- **`dedupe.Do` returns a bare `V`.** Every round used to build an ownership
  cell and hand each caller a counted handle, whether or not anything would
  ever drop it; that was ~500 ns and four allocations of pure ceremony for
  the common case, and the "weave ownership throughout" instinct applied
  where the ownership guidance itself says not to wrap. Ownership of results
  is now a group-level property: a group configured with `WithResultDrop` or
  `WithResultClone` is *owned*, serves results only through `DoShared`
  (one cell per round, counted handles, Drop once after the last release),
  and refuses `Do`/`DoBatch`/`DoBorrowed*` with `ErrOwnedResult` rather than
  letting a copy escape Drop. `DoShared` on a plain group still works, giving
  each caller its own cell over a copy, so the handle API is uniform. The
  round's `execution` is embedded in its `call`, and a candidate that loses
  the registration race now cancels the context it derived, which used to
  stay registered with the base context. `Do` went from 1603 ns / 10 allocs
  to ~1300 / 6 — janos's allocation count, ~220 ns behind it.
- **`async.Map` zeroes a failed item's slot.** Whatever `fn` returned
  beside its error is discarded, so the results slice is deterministic and
  a caller who ignores the error cannot read a half-built value.
- **`async.Map` returns `[]R`.** The 48-byte `Outcome` per item was chosen
  for symmetry with `Gather` and was the entire 1.6x gap to conc. `Gather`
  has labels and heterogeneous tasks; a collection map has neither, and
  failures are the exception, so they are reported out of band as one
  `*ItemError{Index, Err}` per failure in the joined error, sorted by index,
  while the success path touches only the result slot. Within ~8% of conc
  now, and cancellable where conc is not.

## failsafe-go: the boundary, and the bridge (implemented)

The premise singled failsafe-go out — "actually it's maintained and maybe
used directly?" — and it was right to. It is ~10k lines, actively
maintained, and covers retry, circuit breaker, hedge, timeout, fallback,
rate limiter, bulkhead, adaptive limiter, adaptive throttler, budget, cache
policy, priority, and HTTP/gRPC integrations. velocity's `resilience` is
2,250 lines covering three of those. It is a **strict subset**, and the
README now says so rather than implying otherwise: if you want breadth, use
failsafe-go.

Two things survive that comparison, and they are the same thing twice:

- **`Hedge.Discard`.** Their `hedgepolicy` cancels losing attempts'
  contexts but never disposes their *results* — the only cleanup mention in
  the package is about context references. A hedge over a connection
  therefore leaks N-1 of them. That is not a defect in failsafe-go: a
  library that treats the result as opaque cannot know that dropping one
  costs something. velocity can, because `ownership` exists.
- **The `failsafeown` module.** Rather than compete, bridge: make failsafe's
  result type `*ownership.Owner[T]` and release whatever the policy chain
  did not return. It closes the same hole across *their* policies —
  a hedge's losers, a retry rejecting a result by predicate (a leak with no
  error anywhere to hint at it), a fallback dropping the primary's result, a
  timeout returning before a result that arrives anyway. It tracks by
  pointer identity rather than hooking failsafe internals, and keeps
  disposing after `Get` returns, since a hedge's loser can arrive later.
  Deleting the release makes five of its tests fail, which is the check that
  the tests measure the leak rather than describe it. A separate module, so
  the library keeps no dependency on failsafe-go, grpc, or protobuf.

`GetWithExecution` followed from the first consumer trying to use the
module: `Get` alone hands `fn` no `Execution`, so every attempt is
byte-identical and a hedge can only mean "do the same thing again". That is
wrong for the case the package doc *leads with* — a hedge whose replicas
have addresses, which is most of them. The fix mirrors failsafe-go's own
pair rather than inventing a shape, and reuses the tracker unchanged. `Get`
now delegates to it and passes `fn` the execution's context rather than the
caller's, so a losing attempt is cancelled by the policy chain instead of
running on.

One idea taken back the other way: **`LatencyDelay`**, after their
`NewWithDelayQuantile`. A static hedge delay has to be guessed for a
distribution the caller does not know and that moves under load; hedging at
the p95 of recent successful attempts defines "too slow" by what the
dependency has been doing. Theirs uses a t-digest; a ring buffer with
nearest-rank is enough at a hedge window's sample count and needs no
dependency.

Not taken: their adaptive limiter and throttler remain the deferred items
recorded below — but the reason has changed. They are no longer worth
building here at all, because failsafe-go has them and `failsafeown` now
makes them usable with owned results.

## Hedging, and the five libraries not taken (implemented)

The premise's second list — `faustbrian/go-{resilience,hedge,fault-injection,concurrency-limit,bulkhead,adaptive-throttle}` — was read rather than ported. One idea in it was a genuine gap:

- **`resilience.Hedge` (taken).** Retry cannot help an operation that is
  merely slow, because it waits for a failure before acting; hedging starts
  the next attempt while the previous is still running, so p99 falls toward
  p50. Two pieces of it are what make hedging correct rather than merely
  parallel, and both fit velocity better than the library they came from.
  *Disposal*: N attempts produce N results and one is returned, so the
  losers leak unless something disposes them — and when the result is an
  owned resource, `Discard` is simply its `Drop`, which no other hedging
  library is positioned to say. *Budget*: a dependency slow enough to
  trigger hedging is the last one that should get several times its load,
  so each execution credits a token bucket and each speculative attempt
  spends a credit, bounding amplification rather than concurrency. Where
  go-hedge has three mutually exclusive delay modes (`Delay`, `Schedule`,
  `DynamicDelay`) and validation to enforce "exactly one", velocity reuses
  the `Backoff` that `Retry` already has: a fixed delay is a closure, a
  widening one is `ExponentialBackoff`. Scheduling goes through
  `Clock.AfterFunc`, so `ManualClock` drives it. A failed attempt does not
  wait out its delay — the evidence has arrived — which makes Hedge a
  superset of Retry's shape, and an empty budget must still terminate
  rather than wait for a hedge that will never be funded.
- **`go-bulkhead` (not taken).** Bounded partitions with an admission
  policy of reject-immediately or wait-with-timeout. That is
  `async.Semaphore.TryAcquire` and `Acquire(ctx)`, and a registry of
  partitions is a map of them. Nothing to add.
- **`go-fault-injection` (not taken).** A test tool, not a concurrency
  foundation. `ManualClock` covers the determinism velocity's own policies
  need; injecting faults into a caller's filesystem and network belongs in
  the caller's test suite.
- **`go-resilience` (not taken).** A composition layer over retry, hedge
  and budget. Velocity's policies already compose by nesting, which is
  visible at the call site instead of configured elsewhere.
- **`go-adaptive-throttle` and `go-concurrency-limit` (deferred, not
  rejected).** Client-side load shedding proportional to observed reject
  rate, and a `Limit` that learns from latency (AIMD/Vegas/Gradient2)
  rather than being a constant. Both are real: an adaptive `async.Limit`
  would make `Runner` something errgroup structurally cannot be. Both are
  also large, stateful, and only justifiable against a workload that shows
  the static limit is wrong — which no consumer has yet reported. Deferred
  on evidence, not on principle.

## Cancellable locking and what errgroup gave for free (implemented)

The port's last `x/sync` import was `semaphore.NewWeighted(1)`: weight one
at construction and every call, a cancellable mutex, which `sync.Mutex`
cannot be. `pool` with `Max: 1` over `struct{}` would have served, and a
consumer searching for a lock would never have found it there, so
`async.Mutex` and `async.Semaphore` are first-class: `Lock`/`Acquire` wait
under the caller's context, a done context fails even when a permit is
free, and the `Permit` is released exactly once. `lostrelease` knows the
acquirers, including the `Try` forms, whose second result is a bool rather
than an error; there the `ok` branch is where the permit is held, so a
blank permit in `if _, ok := mu.TryLock(); ok` is a leak rather than a
probe and is reported. The analyzer found four such leaks in the type's own
tests on its first run. Weighted acquisition was not added; the only site
that used the weight used one.

Two properties `errgroup` had implicitly were lost in the port and are now
documented at the migration table rather than restored: a joined
`*ItemError` per failure is unusable in a 1 KB status field when every item
failed the same way, so `async.Failures(err)` extracts them in index order
and `[0]` is the one to show; and a `Map` item never claimed, or an
`ErrGroup` function that gets its permit after failure, never runs, so
cleanup for state set up before the fan-out must live in
`Hooks.OnTaskComplete` (fired for unclaimed items with the cause) or a
sweep, not inside the function. That one cost the consumer a silent
permanent leak of an advertised in-flight layer.

## ErrGroup (implemented)

`async.ErrGroup` exists so `x/sync/errgroup` has no remaining reason to be
imported alongside velocity. It keeps errgroup's contract — first error
cancels the group context with that error as cause, `Wait` returns it, a
Limit bounds goroutines by blocking the submitter — and adds what errgroup
lacks: the context is passed to each function; panics become a `*Panic`
error instead of crashing; a function that gets its permit after failure is
not run; `Errors` joins every error in submission order; `WaitContext`
bounds a wait on functions that ignore cancellation; the Runner's Hooks
observe each function. It is built on the Runner so the Limit is stated
once. Benchmarked at parity with errgroup (3.4 vs 3.2 µs unlimited, 4.8 vs
4.8 at a limit of 4) with ~40% fewer allocations; the permit wait is a plain
send checked afterwards, because a `select` against the group context cost
~250 ns per contended permit for a property errgroup does not have either.

## First consumer feedback (implemented)

Periapsis ported seven `conc`/`x/sync` sites to v0.1.0 and reported back.
Three changes came out of it:

- **An abandoned round holds its key until the callback returns.** `leave`
  used to cancel the work and unregister the key in one step, so a callback
  that ignored its context — a third-party provider wedged on some other
  context — let every later caller start another one: a 10 ms ping loop
  stacked 10 flights in a second where `x/sync` ran one. Now cancellation
  is still delivered, but the key stays until `complete`, and a caller
  arriving meanwhile waits under its own context. If the callback then
  succeeded, the caller takes the value (the work was done; it is not
  repeated); if it failed — which for a cooperative callback means it
  reported a cancellation the caller had no part in — the caller starts
  afresh. That is at-most-one-in-flight like `x/sync`, without ever handing
  a caller someone else's cancellation, and without an opt-in. `Forget`
  is the explicit way to start over behind a wedged callback. An owned
  round is never adopted, because its cell was released with its last
  caller.
- **Docs on the round context.** A `v1.Image` resolved inside `Do` kept
  issuing HTTP under the round's context after `Do` returned, and the round
  had cancelled it. `Do` now says so; a value that works after the round
  must be built from the caller's or the base context.
- **Zero-value `Group` and `Must`.** Seven tests built a partial struct
  literal with a `Group` field and nil-panicked, because `singleflight.Group`
  works uninitialised and ours did not. It does now, with every default; a
  `sync.Once` installs them on first use. `dedupe.Must`, `async.Must`, and
  `pool.Must` cover constructors that return no error, in the manner of
  `regexp.MustCompile`, so consumers stop writing the same shim.

## Developer experience pass (implemented)

Four changes from a DX review, all breaking, all free because velocity had
no consumers:

- **`async.Runner`.** `Map(ctx, limit, hooks, items, fn)` made every call
  restate configuration, and `NewPlan` then `Gather` was two steps for one
  operation. `async.New(limit, opts...)` builds a `*Runner` once — an unset
  `Limit` is still `ErrInvalidLimit`, the explicit-bound rule just moves to
  the constructor — and `Gather`/`Race`/`FirstSuccess`/`Map`/`ForEach`/
  `Broadcast` are generic methods on it. `Plan` is gone: it existed to
  validate eagerly and copy tasks for reuse, and nothing reused one. Task
  validation happens per call and still reports `PlanError` with the index.
  A constructor with options was chosen over a struct literal so validation
  has somewhere to run.
- **`ownership.Own(v)`.** `New` with no options cannot fail, so its error
  return was a `_` in every plain use. `Own` is infallible; `New` stays for
  options and is `Own` when given none.
- **`dedupe.New[K, V](opts...)`.** The base context was a required
  positional argument that nearly every caller filled with `Background`. It
  is now the default and `WithBaseContext` the option.
- **`resilience.ManualClock`.** Every test of a `Breaker` or `Retry` was
  going to write the same settable clock; the package's own tests had. It
  is exported, with `Advance`, `Set`, and a `Sleeps` counter so backoff is
  asserted by count rather than by timing.

Follow-ups: `async.Named(label, fn)` replaces the `Task` literal, and
`GatherFuncs`/`RaceFuncs`/`FirstSuccessFuncs` take bare functions for the
unlabeled case, which is most calls. The root package gained a `doc.go`
mapping problem shapes to packages, since first contact was a `Version`
constant. `Scope.OwnCloser` keeps its error return despite the `_ =` it
forces: a resource enrolled after `Close` is silently leaked otherwise,
which is the exact bug the type exists to prevent.

Rejected: a non-error projection such as `owner.Get(func(T) R) R`. It would
have to swallow `ErrConflict`, which is the one signal the package exists to
raise.

## Opcodes and opruntime (implemented)

- `opcodes` defines plain `Op`/`Instruction` data shapes only. No binary
  encode/decode, no `Program` byte format, no domain-specific operations;
  `OpNop` (the zero value) is the only predefined `Op`.
- `opruntime` is registry/dispatch glue, not a virtual machine: `Table`
  maps an `Op` to a caller-supplied `Handler` func, `Table.Dispatch` on a
  single `Instruction` is the primary operation, and `Run` over a
  `[]Instruction` is a thin convenience loop around `Dispatch`. No owned
  registers, operand stack, or execution state.
- `Table` stores handlers in a fixed `[256]Handler` array indexed directly by
  `Op` (a `uint8`), not a map. The `uint8` index into a `[256]Handler` array
  is always in range, so the compiler elides the bounds check entirely.
  Benchmarked (`opruntime/benchmark_test.go`, `BenchmarkTable`): a direct-call
  baseline at ~1.85 ns/op; the original map-backed `Dispatch` at ~26.5 ns/op;
  the array-backed `Dispatch` at ~4.0 ns/op — a 6-7x reduction versus the map.
  All 0 allocs/op throughout.
- `Dispatch` itself never inlines into its caller either way — its two error
  paths construct a `*DispatchError`, which alone exceeds the compiler's
  inlining budget. There is no vtable/virtual-dispatch to eliminate here:
  every `Handler` call was already a plain indirect call through a function
  pointer, not a devirtualizable interface call, since the concrete handler
  is only known at Register time. Moving the `*DispatchError` construction
  into a separate `//go:noinline` helper (`newDispatchError`) keeps that cold
  code out of `Dispatch`'s hot-path machine code and reproducibly took
  array-backed `Dispatch` from ~4.0 ns/op to ~3.7 ns/op — the remaining
  overhead over the direct-call baseline is the one non-inlined call to
  `Dispatch` plus the one indirect call to the registered `Handler`, both
  irreducible without giving up the caller-registered-handler design.
- `opruntime` imports `opcodes` for its types but has zero hardcoded
  semantics. `opcodes` has no dependency on `opruntime` or `ownership`.
  Porting `ownership`'s operations onto `opcodes.Op`/`opruntime` dispatch —
  or any other fixed, known-at-the-call-site op set, such as
  `ownership/model_test.go`'s fuzz-driven op switch — was considered and
  rejected, not merely deferred: a hand-written switch benchmarked ~28%
  faster than `Table.Dispatch` for the same 10-op shape
  (`opruntime/benchmark_test.go`, `BenchmarkSwitchVsTable`), so
  `opcodes`/`opruntime` earn their keep only where the op set is genuinely
  pluggable at runtime (a caller registers handlers, as `dedupe`/`async` do
  not, but a future scripting/replay layer might).
- Named `opruntime`, not `runtime`, to avoid shadowing the stdlib `runtime`
  package already imported by `ownership/owner.go`.

## Instrumentation hooks (implemented)

Replaces the originally-sketched Observer design below with something
simpler: the coalesced dirty-state map and watchdog-timer batch delivery
were for the *library* to own metrics state, but the actual want is for the
**caller** to supply their own instrumentation (route to Prometheus/
OpenTelemetry/logs/whatever themselves). That rules out a generic Observer/
subscription list; the shape used instead is Go's own
`net/http/httptrace.ClientTrace` idiom — a plain struct of optional callback
fields, supplied once at construction, called synchronously and directly
from the goroutine driving the event, zero cost when a field is nil (one
check, no allocation, no interface dispatch, no registration list).

- `dedupe.Hooks[K]{OnJoin, OnComplete}`: `OnJoin` fires for every caller
  that joins a round (leader and followers); `OnComplete` fires once per
  round with the *actual* `fn` execution duration — not any individual
  follower's wait time, which is not knowable outside `Group`. Configured
  via `WithHooks`. `OnComplete` fires after `compareAndDelete`/
  `close(c.done)`, not before — an earlier version fired it first, which
  both delayed every waiter's result on a slow hook and could livelock if a
  hook called back into `Do` for the same key (the entry couldn't be
  removed until the hook, which was waiting on that same entry, returned).
- `async.Hooks{OnTaskComplete}`: fires once per task with `waited` (time
  blocked on a `Limit` permit) and `duration` (`Task.Run`'s own execution
  time) reported separately — the split is only knowable inside
  `execute`/`race`, not from the caller's own timing around `Run`. Threaded
  through `NewPlan` and `Broadcast` as a required parameter (matches
  `Limit`'s own no-implicit-default shape in this package). `race`'s
  producer goroutine already sends to its completion channel before firing
  the hook, so `Race`/`FirstSuccess`'s prompt-return semantics aren't
  affected by a slow hook the way `dedupe`'s original `OnComplete`
  placement was.
- Deliberately not hooked this pass: `ownership`'s hot, heavily-reviewed
  state machine, and `async.Group`/`Pipeline` — scoped out, not an
  oversight; same `Hooks`-struct shape would extend cleanly if ever needed.

Original Observer sketch, superseded by the above:
- Opt-in Observer interface with an allocation/timing-free disabled path.
- Lifecycle counters and transition bits coalesce in a keyed dirty-state map,
  not an event queue; event-driven batch delivery has a configurable one-second
  watchdog fallback.
- Detailed terminal state is removed after delivery while cumulative counters
  remain. Public stats include aggregates and copied active-operation iterators.
- Raw dedupe keys are hidden unless callers configure a safe label projection.

## Dedupe (implemented)

Rewrites the best of `janos`/`resenje.org` `singleflight` and
`samber/go-singleflightx` as `dedupe`, woven through `ownership` rather than
treated as an afterthought:

- `dedupe.New[K, V]` is constructor-required (no zero-value `Group`, unlike
  both source libraries) and takes a base context plus options, including
  Group-level `WithResultDrop`/`WithResultClone` (one Drop/Clone policy per
  key-space, mirroring `ownership.New`).
- `Do`'s result is `*ownership.Shared[V]`, not a plain `V` — every caller of a
  dedup round (leader and every follower) gets its own independently
  `Clone()`d handle, released independently. This replaces dedupe's own
  "everyone left" bookkeeping for the result's lifecycle with
  `ownership.Shared`'s existing handle-count/Release machinery: a configured
  `WithResultDrop` now runs automatically, exactly once, when the last caller
  releases its handle. Fixes a real gap found in janos's fork: it deletes its
  map entry as soon as the waiter count hits zero even if the work is still
  running (non-cooperative), racing a second execution for a later `Do` —
  here, key retention/deletion happens in the leader's own deferred cleanup
  after the work actually returns.
- No `Future` type. Go is already colorblind (no async/await split), and
  `ownership.Shared[V]` already is the reusable/releasable handle a
  Future/Promise would have provided — a caller wanting non-blocking dedup
  just calls `Do` from its own goroutine.
- `DoBorrowed` and `DoBorrowedMut` loan an `ownership.Owner[I]` into the
  leader's work. They acquire before key registration so ownership conflicts
  cannot publish a doomed call, hold the leader's loan for the generation, and
  release it before constructing or publishing `Shared[V]`. A follower briefly
  acquires while determining its role, then releases immediately without
  projecting or mutating its input value. A context cancellation may return
  before a non-cooperative leader releases its loan; `Hooks.OnComplete` fires
  after release when callers need an explicit reuse signal. Consequently,
  concurrent mutable calls using the same Owner may conflict before either
  knows whether it would be the follower; this preserves ownership's
  exclusive-borrow rule.
- `Forget` (stop tracking, in-flight work keeps running) versus `Cancel`
  (actively cancel the in-flight work now) are separate, matching the brief.
- `DoBatch` aligns its output map to the requested keys with real
  `ErrMissingResult` errors (not samber's `Valid bool` flag), and — after a
  review round — routes every key through the same per-key call registry
  `Do` uses, so overlapping `Do`/`DoBatch` calls for the same key properly
  share in-flight work rather than each batch bypassing dedup entirely.
  Multiple newly-led keys within one `DoBatch` call share one `execution` so
  a single key's abandonment can't prematurely cancel work other keys in the
  same batch still need, and can't silently fail to cancel work nobody wants
  anymore either.
- Constructor-selectable backends: `xsync.Map` (default), mutex-map, and a
  sharded mutex-map hashing `K` via Go's generic, seed-based
  `hash/maphash.Comparable[K]` — no caller-supplied hasher needed, unlike
  samber's `Hasher[K]`.
- The default is `xsync.Map` on an asymmetric-payoff argument, not a clean
  win: benchmarked across three workloads it is ~1.9x mutex when goroutines
  register distinct keys concurrently and ~10% faster on one contended key,
  but ~8% *slower* uncontended and one allocation heavier everywhere. The
  large win outweighs the small loss for code that reaches for a dedup
  library at all. `WithMutexBackend` is explicitly **not** deprecated — it
  is the measured best choice for low-concurrency and allocation-sensitive
  callers. An earlier backend benchmark measured only the key-per-goroutine
  case, the one workload mutex loses badly; deprecating mutex on that
  evidence would have been a conclusion drawn from the single scenario that
  supported it, so `benchmarks/` now covers all three.
- `dedupe.Singleflight[K, V]`/`NewSingleflight` are exact aliases of
  `Group`/`New`, for readers who know this pattern by its more common name
  (same idiom as `ownership.Release`/`Close`).
- Panic handling follows conc's `panics.Catcher` pattern (recover, capture
  stack, first-panic-wins, re-panic in the waiter's own `Do`/`DoBatch` call)
  through one shared internal helper, rather than samber's duplicated
  crash-forcing logic or janos's bare recover-and-close.

## Async and resilience (implemented)

Rewrites the best of `AaronJan/Hunch` and `sourcegraph/conc` as `async` +
`resilience`:

- `async.Limit`/`Unlimited`/`Limited` force callers to say what they mean
  about concurrency bounds; `NewPlan` validates eagerly and copies its tasks
  so the returned `Plan[T]` is genuinely immutable. `Task[T]` carries an
  optional `Label`; `Outcome[T]` carries the source `Index`, `Label`,
  `Value`, and `Err` — stable index plus label, as specified.
- `Gather` reserves each task's output slot eagerly at scheduling time
  (conc's `resultAggregator` pattern), not Hunch's completion-order-then-sort
  — source-index order with zero post-hoc sorting, errors joined via
  `errors.Join`.
- `Race`/`FirstSuccess` return promptly on the first completion (matching
  Hunch's actual `Take` behavior and the brief's "first completion wins"),
  draining canceled siblings into an already-buffered completion channel
  rather than blocking the return on `wg.Wait()`. A non-cooperative sibling
  (one that ignores `ctx.Done()`) may keep running in the background after
  return — documented, not hidden. `Take`/`Last` are recipes over `Gather`'s
  result slice, not separate API functions.
- `Broadcast[T, R]` is the concrete ownership integration point: it fans one
  `*ownership.Owner[T]` input out to N concurrent workers by relying on
  `Owner[T].Read` already permitting concurrent scoped reads — zero new
  ownership primitives needed, genuine reuse of an existing guarantee.
- `Pipeline[T]`'s `Then[R any]` is a Go 1.27 generic method (declaring its
  own type parameter beyond the receiver's `T`), giving a fluent,
  heterogeneously-typed chain; stages fail fast under one run context.
  `Pipeline` is this package's Waterfall — no literal `Waterfall` alias
  exists because Hunch's variadic `Waterfall(ctx, stages...)` requires every
  stage to share one type, which is incompatible with `Then` changing type
  per stage; documented at the `Pipeline` declaration instead.
- `async.Group` wraps stdlib `sync.WaitGroup.Go` (native since Go 1.25,
  confirmed present in Go 1.27) with conc-style panic recovery — the stdlib
  version is a bare `Add`/`Done` wrapper with no `recover()` at all.
  `Close(ctx)` decrements an active-op counter and closes one shared
  terminal channel once it hits zero, so concurrent `Close` callers share
  one channel instead of each spawning their own waiter.
- `resilience.Retry` takes an explicit `Policy` (required positive
  `MaxAttempts`, optional `Classifier`, `Backoff`, injectable `Clock`).
  `ExponentialBackoff(base, max, jitter) (Backoff, error)` validates eagerly
  (house style: fail fast at construction, not first use) and — after a
  review round caught a real bug — computes into a fresh local per call
  instead of mutating its captured `base` parameter, which had been silently
  corrupting every subsequent call's delay.
- `async.Map`/`ForEach` are conc's `iter.Map`/`ForEach`, and deliberately not
  `Gather` over a generated `Plan`: a plan is distinct labeled tasks and gets
  a goroutine each, a collection is one function over many items and gets a
  fixed pool of `Limit` workers pulling indices off an atomic counter. The
  difference is 12x at 1024 items with constant allocations. `Map` returns
  the same `Outcome` as `Gather` so a caller learns *which* items failed;
  that 48-byte record is why it trails conc's bare result slice by ~1.6x, a
  price paid knowingly and recorded in `benchmarks/README.md`. Clock reads
  for `Hooks` are skipped when no hook is set. There is no `MapIndexed`; the
  index is on the `Outcome`, and in-place mutation of the input is the
  ownership-shaped case of `Map` inside `View` producing values written back
  under `WithWrite`, not a `*T` callback.
- `resilience.Breaker` follows the package's rule that nothing waits: a
  rejected call returns `ErrOpen` at once, and transitions are applied
  lazily by the next call or `State` read rather than by a timer goroutine,
  so a breaker is inert when idle and its `Clock` is the only time source.
  Reports carry the generation they were admitted under and are discarded if
  the state changed meanwhile, so a slow probe from a window already judged
  cannot reopen a breaker that has since closed. `Do` is a generic method;
  `Allow` exists for calls that cannot be wrapped, and costs the exactly-once
  closure `Do` avoids (`Do` closed path: 0 allocs). A panicking callback is
  reported as a failure before propagating, because the alternative is a
  half-open probe slot occupied until the next transition. `Failure` is nil
  by default and counts every error, including the caller's own
  cancellation — explicit over guessing, with the exclusion recipe on the
  field. Rate limiters are not planned: `x/time/rate` already does the job
  and velocity would add nothing but a wrapper.
- `pool.Pool[T]` is the concrete primitive behind "resources held and
  returned", the one shape in the ownership guidance that had none. A
  `Checkout` embeds `*ownership.Lease[T]` rather than wrapping it, so
  release-exactly-once, use-after-return detection, `Move`, and `io.Closer`
  (hence `Scope.OwnCloser`) are inherited rather than re-implemented;
  `Discard` is the only addition, a flag the lease's release closure reads to
  destroy instead of return. Capacity is a permit channel covering idle and
  checked-out resources together, taken before construction and returned
  after the idle set is updated, so a waiter admitted by a release finds the
  returned resource already there. Idle reuse is LIFO, warm first. `Close`
  destroys idle resources and refuses new `Get`s but does not wait for
  outstanding checkouts, which are destroyed on return — the same
  non-waiting split as `Seal`/`Drained`, and for the same reason. No health
  check on `Get`: the caller who used the resource knows whether it is
  broken, and says so with `Discard`.
- Root `Task`/`Outcome`/`ID` registry defaults remain future benchmark
  decisions, not committed API, per the original brief.
