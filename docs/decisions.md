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
- Constructor-selectable backends: mutex-map (default), `xsync.Map`, and a
  sharded mutex-map hashing `K` via Go's generic, seed-based
  `hash/maphash.Comparable[K]` — no caller-supplied hasher needed, unlike
  samber's `Hasher[K]`.
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
  corrupting every subsequent call's delay. Circuit breakers/limiters remain
  deferred, as the original brief specified.
- Root `Task`/`Outcome`/`ID` registry defaults remain future benchmark
  decisions, not committed API, per the original brief.
