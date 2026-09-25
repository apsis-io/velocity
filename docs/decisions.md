# Current design decisions

This records current selections, not superseded planning alternatives.

## What the evidence in this record is, and is not

Several entries below cite a report from a project using velocity: a port that
hit a wall, a migration that got awkward, a shape that turned out to leak. Those
reports are the strongest evidence this library has, and they are all real
workloads with real failures behind them.

They come from the author's own projects. That does not weaken them as evidence
of a **defect** — a poll that under-waits its budget is wrong whoever reports it,
and every claim in those entries was checked against the source or a benchmark
rather than taken on trust. But it makes them worth nothing as evidence of
**demand**, and the distinction is easy to blur because "a consumer reported
this" sounds like the two at once.

So, read the record with this in mind:

- A report justifies **fixing or working around a failure**. It is a
  measurement of something that already went wrong.
- A report never justifies **adding an API**. Nobody outside this arrangement
  has asked for anything, and a closed set of consumers cannot produce evidence
  of absence either — "no one has asked for it" carries no information when the
  only possible askers are reachable from here. Features are justified by the
  author's judgement or by a measurement, not by a hypothetical request.
- Where an entry says a decision is "deferred on evidence, not on principle,"
  the evidence means a measured failure of an existing shape, not a demand for a
  replacement.
- The one place this bites hardest is a comparison against another library: a
  defect found in a third-party library is evidence about *that* library and says
  nothing about whether velocity should grow to cover it.

## Repository and traits

- Module: `github.com/apsis-io/velocity`, Go 1.27 only, MIT license held by
  Malformed C, development `Version = "dev"`.
- No retained or updated upstream clones, hosted CI, GoReleaser, reflection
  registry, inheritance framework, or generated API unless repetition later
  proves generation useful.
- **Amended: the exclusion of external linters was too broad, and had already
  been contradicted before it was written down.** `staticcheck` has been in CI
  and in the justfile since the first entries in this record, so the line was
  describing something the repository did not do. It is amended rather than
  left to be discovered a second time, and the line above now says what the
  rule actually is.
- **Two external linters, and why each earned its place.** `staticcheck` for
  correctness — it caught a suppression written in golangci-lint's directive
  dialect, which staticcheck does not read, on two release candidates in a row
  that every other check passed. `wsl` for whitespace: a blank line where a
  block ends and a new statement group begins, which is the one style rule here
  that nothing enforced. `wsl` is run through `golangci-lint`; `staticcheck`,
  `go vet` and velocity's own `lostrelease` are still run directly, each pinned.
  That split is temporary and is discussed below.
- **Whitespace style is `wsl`, run through `golangci-lint`, with its default
  linter set on.** The style itself is one rule repeated: a blank line where a
  block ends and a new statement group begins. Applied across every module it
  changed 2321 sites over 96 files, of which 2267 were blank lines and 54 merged
  adjacent `var` declarations into a `var (...)` group — semantically inert in
  Go, which was verified with the suite, `-race`, and a benchmark rather than
  assumed.
- **It is run by `golangci-lint` rather than as a standalone `go run`, and
  that changed three things worth recording.** The first configuration used a
  pinned `go run github.com/bombsimon/wsl/v5/cmd/wsl@v5.9.0`, which worked; the
  switch to `golangci-lint` came from wanting the bundled `wsl_v5`, whose
  version tracks the tool so there is no separate pin to keep current. The
  standalone path is also why the version matters: v4 bundles an `x/tools` too
  old to read Go 1.27 export data and dies with `package strconv without types`
  **before reporting anything**, so an apparently-current version silently
  reports nothing at all. That failure mode — a linter that cannot parse the
  toolchain and exits with a message about a package it never read — is the
  reason the version is checked rather than trusted.
- **Enabling `golangci-lint`'s default set surfaced 34 findings the repository
  had never been checked against, and that is the argument for having done it
  one linter at a time.** 26 were `errcheck` on a deferred `Release()` or
  `Close()` in a test or benchmark, which is the ordinary Go idiom and is
  excluded by path — `analysis/lostrelease` is what covers whether the handle
  was released, and it runs over test files too. One was in library code:
  `dedupe/borrowed.go` deferred a borrow release and discarded its error, which
  the two sibling paths in the same function already discarded explicitly, so
  the fix was to make the third consistent rather than to decide anything. Two
  were `staticcheck` style suggestions on redundant types, fixed. Five were
  suppressions this repository already had: three `SA2001` in a benchmark file
  and two `SA1012` in nil-context tests, both carried as `//lint:ignore` for
  standalone `staticcheck`, which honours that directive and which
  `golangci-lint`'s copy does not — so they are now listed in both places.
  **Two staticchecks that disagree about suppressions is a cost of running
  both**, and consolidating on one is the obvious follow-up.
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
2,250 lines covering three of those. In **policy coverage** it is a subset
and the README now says so rather than implying otherwise: if you want
breadth, use failsafe-go.

*An earlier version of this entry called it a "strict subset" and then
listed, two paragraphs down, the things that are not in it. The comparison
behind the phrase was of policy names, which is a narrower population than
the noun claims; a strict subset has nothing outside it. The scope is now
in the sentence.*

Two things survive that comparison, and they are the same thing twice:

- **`Hedge.Discard`.** Their `hedgepolicy` cancels losing attempts'
  contexts but never disposes their *results*. Read from the executor
  rather than searched for: a losing result is sent to a `resultChan`
  buffered at `maxHedges+1`, overwritten by `lastResult`, and dropped with
  the channel, while the only cleanup is `execution.Cancel`, which its own
  comment scopes to "their context references". A hedge over a connection
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
  Deleting the release fails seven of its tests, which is the check that
  they measure the leak rather than describe it. A separate module, so
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

The first use of `GetWithExecution` then found a trap in the API it
exposes, and the trap was in *velocity's own example*. `failsafe.Execution`
offers both `Hedges()` and `IsHedge()`, and the one with a number in it
reads like an attempt index; it is not. `hedges` is a `*atomic.Uint32`
under "Shared state across instances" and counts hedges that exist,
in-progress included, while `isHedge` is a plain per-attempt bool. Using
`Hedges() == 0` as a discriminator sends both arms down one branch, so the
other never runs — a hang in a pull path, not a wrong answer. It is also
invisible under a long hedge delay, because then the primary reads the
counter before the hedge exists, which is exactly why velocity's own test
passed: it used a 10 ms delay. The test now uses a zero delay, which is
what a plain race wants anyway, and fails on all three runs if the
discriminator regresses. Documented on `GetWithExecution` and in the
README, alongside the other default worth knowing: hedge's `Build`
installs "cancel on any result", so without `CancelIf(err == nil)` the arm
that fails fastest ends the race.

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
  losers leak unless something disposes them. The idea is go-hedge's, not
  velocity's — it has a `Disposer[T]`, invokes it from six sites, and
  reports cleanup failures as a counted `CleanupError`. What velocity adds
  is narrower: because the value is an `ownership.Owner`, `Discard` is the
  `Drop` already attached to the resource, so there is no second place to
  state cleanup and no way for the two to disagree.

  *An earlier version of this entry said disposal was something "no other
  hedging library is positioned to say", in a paragraph that names
  go-hedge. That was false about the library it credits. failsafe-go is the
  one that does not dispose, and that claim now rests on reading its hedge
  executor — losing results land in a buffered channel and are overwritten,
  and the only cleanup is `execution.Cancel` on contexts — rather than on
  the keyword grep that first suggested it. A grep for cleanup words cannot
  see a disposal mechanism that uses different words.* *Budget*: a dependency slow enough to
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

## The analyzer learns its acquirers instead of listing them (implemented)

`lostrelease` picked acquirers from a hand-written table of names, which
could not be searched for something not yet named: a new acquiring method
in `ownership`, `pool`, or `async` would be silently unchecked, with the
analyzer and its tests both still passing. A drift test now shouts about
that, but the classification still lived in another module from the code it
described.

It is now a directive at the declaration — `//velocity:acquires` — which Go
hides from godoc by its own rules, published as an `analysis.Fact` so it
reaches consumers, who see velocity through export data and never its
comments. The mechanism is not velocity-specific: any library can mark its
own handle-returning functions, and a test proves that with a package the
table has never heard of.

Two things had to be measured rather than assumed, and the first guess was
wrong both times:

- **Does `go vet` analyse dependencies at all?** Yes — instrumenting the
  analyzer showed it running over the whole build graph, stdlib included,
  and reporting `marked=true` for velocity's packages.
- **Do object facts survive generics?** No. A fact exported on
  `Owner[T].Borrow` is keyed on the declaration; a call site yields the
  method of an *instantiation*, a different object, and `Func.Origin` does
  not bridge them — verified by the non-generic `async.Mutex.Lock` working
  while the generic `ownership.Owner.Borrow` did not, which is what made
  the cause unambiguous. The fact is therefore a **package** fact holding
  qualified names; a package has no instantiations. Proven by emptying the
  table entirely and confirming both are still reported.

The table stays as a fallback, because v0.4.0 and earlier carry no markers
and consumers are on them.

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

One note on how the reports in this section were read. Nearly every claim
in them arrived with an instrument behind it — flight counts, allocation
counts, mutation runs — and the accuracy was high enough that an
unmeasured sentence sitting among them inherited the same confidence.
Exactly one did, and it shaped the *urgency* of a change below before the
consumer retracted it. A passing test cannot contradict a claim about who
implements an interface, so a claim of that shape needs asking about
directly rather than being carried along by the numbers around it.


Periapsis ported seven `conc`/`x/sync` sites to v0.1.0 and reported back.
Three changes came out of it:

- **An abandoned round holds its key until the callback returns.** `leave`
  used to cancel the work and unregister the key in one step, so a callback
  that ignored its context let every later caller start another one: a 10 ms
  ping loop stacked 10 flights in a second where `x/sync` ran one.

  *Corrected after the fact.* This originally read "a third-party provider
  wedged on some other context", repeating the consumer's framing. The
  consumer retracted it: the implementations all live in their own repo and
  none of the production ones can block, so the wedged provider is the
  test's fake and the hazard is inherited from the upstream project the
  fork came from rather than observed. (Their first retraction enumerated
  four implementations; a later one found a fifth its census pattern could
  not match. The count is theirs to hold and is not restated here, because
  a fact about someone else's tree does not belong in velocity's design
  record — only the conclusion it supports does.) The measurement stands — it measures
  velocity, not their providers — but it measures a scenario nobody there is
  in today, and the reasoning below is what actually carries the change.

  It stands on the library's contract, not on any consumer's exposure.
  `Singleflight` is an alias that invites a straight swap from
  `x/sync/singleflight`, which holds a key until `fn` returns; without
  retention that swap silently changes behaviour for any callback that does
  not honour cancellation, and such callbacks are ordinary — an HTTP client
  with no context plumbed through, a cgo call, a sleep loop. A library
  cannot assume its users' callbacks cooperate. The value-adoption and
  failure-restart semantics came from a separate soundness argument, that a
  joiner must never receive another caller's cancellation, and were not
  motivated by exposure at all. Now cancellation
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

## ErrGroup's stream shape, found by asking whether `routing` needed a primitive (implemented)

The question was whether velocity needed a stream-shaped primitive at all. A
bounded message consumer reads a channel and submits each item under a fixed
in-flight bound; `async` is collection-shaped (`Map`, `ForEach`, `Gather` over a
known slice), and the first prototype of a consumer hand-wrote an
`async.Semaphore` loop because nothing in the package appeared to fit.

`async.ErrGroup` does fit, and the prototype had not read it closely enough.
`Go` takes a permit so the `Limit` bounds goroutines rather than only running
work, and `WaitContext` bounds the drain for functions that ignore their
cancellation. A consumer is four lines, and it inherits panic recovery, sibling
cancellation on first error, and every error in submission order. **There is no
missing primitive, so no package was added to hold one.** A separate evaluation
of watermill measured the same loop and the same shape, which is the outcome:
the advantage is in composing, not in a new category.

Measuring it anyway turned up two gaps, both in the stream shape and both
contained:

- **A stream consumer could not be shut down in bounded time.** `Go` takes its
  permit with a plain blocking send, chosen deliberately over a `select`
  against the group context because the `select` costs ~250 ns per contended
  permit. For a submitter with work it must run and no reason to stop, that is
  the right trade. For a consumer reading from a channel it is the other end
  of a producer that may still be publishing, holding a shutdown context, and
  required to be able to stop: with every permit held by a function that
  ignores its own cancellation, the loop cannot reach its cancellation branch,
  and so never reaches `WaitContext` — the documented way to bound exactly that
  wait is unreachable from the shape that needs it. Measured over three handler
  regimes, both `ErrGroup` and the hand-written `Semaphore` loop were still
  running past 8 s against a handler that never returns; the loop does not fix
  it either, since it substitutes a bare `wg.Wait` for the same unbounded
  drain, so the gap belongs to the shape rather than to either arm.
  `GoCtx(ctx, fn) bool` is the addition: it selects on ctx for the permit
  and reports whether the function was submitted. `Go` is unchanged, because
  its cost is the reason the method is not the default — measured at a free
  permit, which is the common case, `GoCtx` costs ~35 ns more of 1145
  (1177 vs 1145, 3 runs, 985k–1M iterations, same 480 B and 6 allocs), and the
  contended case is where the documented ~250 ns applies. That case does not
  isolate in a benchmark, since holding a permit needs a holder and releasing
  it needs a timer, so the measurement would be of the holder.
- **Items were dropped silently.** A function that obtained its permit after
  the group was cancelled is not run, which is documented and correct in
  itself; but `Hooks.OnTaskComplete` did not fire for it, so a consumer that
  read an item from a channel and did not run it had no way to find out, and
  `Wait` returned nil. In every measured regime both arms completed fewer items
  than were fed and reported nothing — 56 of 200, in the regime where permits
  cycle fast enough that nothing can wedge. The `Hooks` contract already said a
  task that never starts is "reported from the caller's", and `Map` honoured
  that; the group did not, so this was a divergence from the package's own
  documented behaviour rather than a new feature. All three submission paths
  now report a submission that will not run, with the group's cancellation
  cause and zero duration.

One consequence worth stating, because it looks like a bug and is not: a
submission that never ran reports the group's cancellation *cause*, so a group
whose first failure was `boom` reports `boom` for the submissions that follow
it. That is what the cause is, and it is why the regression test cancels through
the parent context instead — a test that classified by error value would count
the skipped submissions as failures and pass for the wrong reason.

Not taken: a counter of skipped submissions alongside the hook. The hook already
reports each one, and two places to state the same thing is how they come to
disagree.

Not taken: changing `Go` itself to select on the context. It is the fast path,
it is what `x/sync` does, and the reason the bounded form is a separate method
is precisely that callers who need to stop should say so.

## The integer standing in for a bound, and the condition form (implemented)

A consumer of velocity — breeze, porting four hand-rolled "poll until a
deadline" loops onto `resilience` — reported that a required `MaxAttempts` was
not the redundancy it looked like, and three of its arguments were checked
against the source rather than taken on faith.

- **The attempt count binds first, always.** `Retry` returned `*RetryError` the
  moment `attempt == MaxAttempts`, with no final sleep and no consultation of
  the context, while the `ctx.Err()` check sat at the top of the next attempt.
  So the only derivation available to a caller — `attempts =
  budget/interval + 1` — ended the poll *up to one interval short of its own
  budget, silently*, and their ratio matrix had 5 of 6 cases ending on the
  count rather than the deadline. At breeze's ratios (20–100 ms against 15–20 s
  budgets) the bias is harmless, and it is a bias rather than a bug in their
  code. Structurally it is exactly the failure worth removing: the loop stops,
  and the reason it stopped is a number somebody derived rather than a bound
  anybody stated.
- **Unwrapping the give-up as `context.Cause(ctx)` reports success.** At the
  give-up point the context is still alive, so `Cause` returns nil, and a
  wrapper that re-derives the cause that way returns a zero value with a nil
  error. It escaped notice because a test asserted the invariant across the
  ratio matrix and every ratio hit it; a keyword reading of the package would
  not have found it.
- **An error-shaped predicate inverts a condition.** "Retry until the lock is
  held" is not "retry until the operation succeeds", and forcing it through
  `Retry` needs a sentinel error for "not yet", a classifier recognising that
  sentinel, and a `struct{}{}` invented to satisfy `T` in the three loops with
  nothing to return.

All three are one cause, which is the part worth recording: a required integer
standing in for a bound. The fix is not to relax the house rule but to say
where the bound may be stated.

- **`Policy.MaxAttempts == 0` means the caller's context is the bound.** A
  positive value is still the attempt count, so `Retry` keeps its existing
  shape, and a policy that bounds *nothing* — zero attempts against a context
  with no deadline — is `ErrInvalidPolicy` wrapping `ErrNoBound`. The rule is
  preserved rather than bent: a caller still says what it is bounded by, and
  saying "my context" is now one of the ways to say it.
- **One give-up error, so the answer does not depend on arithmetic.** Every
  give-up in the package is a `*RetryError` satisfying
  `errors.Is(err, ErrGaveUp)`: the attempts exhausted, the context ending the
  loop at the top of an attempt, and a backoff sleep the context interrupted.
  The last of those used to return the bare context cause, so *which* type a
  caller received depended on whether the count or the deadline happened to
  bind first — and the obvious remedy for the under-wait, rounding a derived
  count up, moves the loop from one bind to the other and so changed the error
  type every downstream caller received, without touching a line of theirs.
  A rounding fix reading as a performance tweak and landing as a compatibility
  break is the whole argument for `ErrGaveUp`. The detail stays reachable:
  `Unwrap` yields both `ErrGaveUp` and `Last`, so `errors.Is(err,
  context.DeadlineExceeded)` still works, and `Last` is never nil on a give-up,
  which is what makes the `context.Cause` trap unrepresentable rather than
  merely discouraged.
- **`RetryUntil[T]` for "until this is true".** `probe func(context.Context)
  (T, bool, error)`: satisfied returns the value, a non-nil error is a failure
  and ends the loop unchanged (a probe that wants an error retried returns
  satisfied false with a nil error), and otherwise the bound applies exactly as
  in `Retry`. There is no sentinel to invert and no classifier to recognise it.
  `UntilPolicy` is a separate type from `Policy` rather than a reuse with a
  field ignored: a `Retryable` on a condition form would be set in good faith
  and obeyed by nobody.

The late validation is a real inconsistency, and worth naming rather than
hiding. `validBound` cannot run at construction, because the context is an
argument and `Policy` does not carry one, so a policy that bounds nothing fails
at first use. That is against the eager-validation style the rest of the
package follows. It is kept because it is the moment the information is
actionable — it is when a caller discovers their bound never bounded anything
— and because the alternative is forcing every deadline-bounded caller to
compute the integer whose cost this section is about.

Not benchmarked: `RetryUntil` against a hand-rolled poll. It is the same loop
as `Retry` with one more return value from the probe, and the consumer's own
numbers were the ones worth having.

The consumer is re-porting against this shape, which is the point of recording
it here rather than only in review.

## Request/reply, as a shape and a test rather than a package (implemented)

The `routing` evaluation measured the sharpest single result in the whole
exercise: 25 callers that each read one reply of two and then abandoned it left
**50 goroutines behind** in watermill's `requestreply`, against +3 for the same
loop with the caller cancelling and **+0** when the responder owns the cleanup.
Nothing in velocity's `async` leaks there, and nothing in it stops a caller
leaking there either — the property was a property of a shape nobody had written
down, which is the worst place for one to live.

It is now written down, as `Example_requestReply` and a test beside it, with the
registry in the test file rather than the package. That placement is the
decision: the measurement says what the shape must be, and no consumer has asked
for a request/reply API, so a type would be a category invented on the strength
of a benchmark. An example is godoc-visible and commits to no surface. If someone
wants the type, the measurement is already here to justify it.

Three things the shape has to get right, each of which is a way to strand a
goroutine:

- **The removal belongs to the responder.** The caller creates the entry and
  then hands the removal away, so the caller's own exit path — including
  forgetting, which is the whole case — is not on the critical path. A `defer`
  runs; a cancel nobody calls does not.
- **Delivery never blocks the responder.** A second reply to a caller that has
  read one is dropped rather than parked on a full channel, because that blocked
  send is precisely what stops the responder reaching the `defer` that would
  have cleaned up. The leak and the fix are the same line of code.
- **A submitter that must stop uses `GoCtx`, not `Go`.** A loop calling `Go`
  blocks on the permit with a plain send, so with every permit held it cannot
  reach its own cancellation branch — and a loop that cannot reach its cancel
  can never shut down.

The third is worth reading twice, because writing the example tripped it. The
first version submitted three responders from the example's own goroutine with
the limit at two, so the third `GoCtx` blocked and the loop never reached
the `cancel()` two lines below it: the example hung until the test timeout, and
the responders were still parked on a context nothing had cancelled. A hazard
documented in a package is not thereby avoided by the package's own example. The
blocked submission now happens on its own goroutine, which is what a real
consumer's submit loop is anyway, and cancelling from elsewhere releases it.

The test asserts the thing that matters rather than the thing that is easy to
assert: no goroutine counting, since that is timing-dependent and would flake
under load. It waits for every responder to return and then asserts the registry
is empty. Twenty-five abandoned calls, zero left behind.

## ownership is kept deliberately, with no call site to point at (decided)

`ownership` is 4,559 lines, the largest package here by a wide margin. It was
recorded as having **zero sites across both consuming projects**, and that was
wrong — in the direction that understates it. Checked against the trees rather
than the reports that prompted the entry:

- **Periapsis, `internal/image/pull.go`, six call sites** — counted as
  non-test lines that are code rather than prose, with a builder chain counted
  once. (This was first recorded as **ten**, which was a count of `grep`
  matches including comment lines and the `cmd/perigeos` prose. The count was
  never the point; asserting a number measured the wrong way was.)
  A layer-pull race where
  each side's result is an `ownership.Owner` whose `Drop` removes that side's
  temp dir and blob, carried through `failsafeown.GetWithExecution` so that every
  owner the policy chain did not hand back is released — **including one that
  arrives after the winner was chosen**, on the goroutine that produced it. That
  is the `failsafeown` use case this record describes, in production, with the
  late-loser case the hand-written code had to remember at every call site.
- **breeze, `daemon_lifecycle.go`, two sites.** `ownership.NewScope` for a
  four-branch unwind in `tryBindDaemon`, where the flock-then-close order becomes
  a property of acquisition rather than of each error path.

What is actually unadopted is narrower and more specific: **the borrow
machinery.** Periapsis wrote a full swap of a registry to
`ownership.Owner[pawnTable]` — `Seal`, `Drained`, the absorb/propagate split,
the sealed table wired to real sealed behaviour — and reverted it, because a
registry admits by queuing and `Mutate` refuses. So the record's claim was
right about the *borrows* and wrong about the *package*, and the two had been
run together.

**The strongest evidence that this record is load-bearing is that the consumer
quoted it to make the decision.** `pawnregistry.go` carries a written
justification for using `async.RWMutex` instead, and it argues from the
no-wait invariant and from the absence of channels, selects and context waits in
this package — the argument above, applied correctly by someone who had read
it. A design record that a consumer can cite in a code comment is doing its
job in a way no test can check.

Which leaves the earlier framing wrong in its conclusion as well as its facts.
The package was kept on the strength of a judgement and a differentiator, with no
call site to point at. It has two call sites, in the two shapes it was designed
for, and one documented negative from a consumer that looked hard and wrote down
why. That is a better position than the one this entry originally recorded, and
it was available the whole time.

**It encodes a discipline whose failure mode is forgetting.** A team using
`x/sync` plus good habits gets the primitives and the intention. What it does
not get is the forgetting caught: a conflicting access reported as `ErrConflict`
at once rather than waited out, a `Detach` whose name says that cleanup will
never run, a `Scope` that releases in reverse order and joins the failures, a
`Lease` that refuses use after release, and `analysis/lostrelease`, which
reports a handle assigned to `_` and now learns its acquirers from a
`//velocity:acquires` directive at the declaration rather than a hand-written
table. The claim is about the class of bug, not a site: a leak that is detected
when it happens is worth more than one that is documented against.

**It is the differentiator.** Everything else in this repository is reachable by
a careful team from the standard library and `x/sync`. This is the part that is
not, and a library whose pitch is "the primitives, arranged well" has no reason
to exist at all.

**What this costs, stated plainly.** With no call site, the package is exercised
only by its own tests. That is a real risk and it is not hypothetical: a
discipline encoded in a type that nothing calls tends to drift toward whatever
the tests happen to check, and the tests are written by the same person who
wrote the type. The record is the only thing that will notice, which is part of
why the entry exists.

**What would reopen it.** A call site settles the question in one direction — the
friction argument from the earlier repositioning ("callers already have `defer`,
`Close` and scoped cleanup, so wrapping a value in `Owner[T]` could cost more
than the problem it solved") has been answered by whoever writes it. Continued
absence across further projects argues the other way, and at that point the
honest options are to shrink the package to the parts with sites or to say
plainly that it is a bet being paid for on a schedule rather than on evidence.
Either is defensible; leaving it unremarked is not, because an unremarked
absence reads as an unanswered accusation rather than a decision taken.

Note the difference from the entries above: those were changed *because* a
consumer reported a failure. This one is kept *despite* the absence of one, and
that asymmetry is the whole content of it.

## async.RWMutex, on judgement rather than evidence (implemented)

`RWMutex` is a read/write lock whose `RLock` and `Lock` both wait under the
caller's context, which `sync.RWMutex` cannot do. Added because a consumer was
about to guard a read-mostly structure with `sync.RWMutex` and wanted the
velocity equivalent.

**This is the author's-judgement path, and it is the weaker one.** The entry at
the top of this record says a report never justifies adding an API. This is a
report, and it is being acted on anyway because the author asked for it — which
the same entry permits ("justified by the author's judgement or by a
measurement"), but the distinction is worth stating rather than blurring. The
honest position is that nobody has measured a site this wants.

**Measured against `sync.RWMutex`, it loses in every regime tested** (Xeon
E5-2690 v4, Go 1.27, both arms interleaved in one process):

| | async | sync |
|---|---|---|
| uncontended read | 85 ns, 48 B, 1 alloc | **12 ns, 0 allocs** |
| uncontended write | 86 ns, 48 B, 1 alloc | **27 ns, 0 allocs** |

**Correction: the contended rows in the original version of this entry were
measuring the harness.** They spawned a goroutine per iteration inside
`b.Loop`, so each sample included the cost of creating a goroutine and joining
it — hundreds of nanoseconds against a lock that takes eleven — and the
conclusion that RWMutex loses under contention was a measurement of `sync.WaitGroup`
rather than of either lock. Re-measured with the workers started once and looping
(`BenchmarkRWMutexParallel`), the uncontended figures above hold and the contended
ones come out at async 351 ns against sync 35 on reads, 177 against 130 on
writes.

**A second measurement, on another machine, agrees — and the first version of
this entry said it did not.** That was wrong in a way worth recording. A consumer
benchmarking the same contended read reported 28.9 ns for `sync` and 326.8 ns for
`async`, and it was read here as the figures reversed: sync slow, async fast,
"same ratio, opposite sign, two machines", followed by a paragraph asserting that
under contention an atomic counter and a mutex trade places on core count and
topology and that neither figure transfers.

**They do not disagree. They measured the same thing twice and both say `async`
is about 10x slower under contention** — 351 ns against 35 here, 326.8 against
28.9 there. The conclusion this entry had drawn, that the contended direction is
unresolvable, was an artefact of transposing two numbers in a message and not
checking the direction before writing a paragraph about it.

That is the same failure this record keeps finding in one form or another — a
claim about a number, written from reading rather than from re-deriving — and it
is worse here than most, because the sentence it produced tells the next reader
that a question with a measured answer is an open one. **A portable question
answered twice the same way is settled, and a summary of someone else's
benchmark is a claim to be checked, not a conclusion to be built on.**

**Both numbers are portable, and the uncontended one is the one that decides a
decision.** It reproduces across the two machines — 7.3x here against 8.2x there.
A consumer whose registry serves 19 reads a minute pays about a microsecond a
minute for the gap, which is why they kept the type despite it: the cost that is not negligible
is the complexity, and that is paid once and written down. The cancellability is
what is being bought, and it costs ~7x on the fast path.

**The cost falls on the existing hot path.** `Permit` grew two fields so
`Release` can tell a read lock from a write lock without a closure:

| | before | after |
|---|---|---|
| `Semaphore.Acquire` | 117 ns, **24 B**, 1 alloc | 118 ns, **48 B**, 1 alloc |
| `Mutex.Lock` | 121-134 ns, **24 B**, 1 alloc | 121 ns, **48 B**, 1 alloc |

No measurable time change — the deltas are inside run-to-run variance — but the
allocation doubles on every `Semaphore` and `Mutex` acquire, to serve a type that
loses its benchmark. An earlier version used a `func()` release hook and cost two
allocations per lock acquire instead of one; two fields beat a closure because
the closure escapes. A separate lock handle type would avoid the `Permit` growth
entirely at the cost of a second release type, and was not chosen because
one release type with release-exactly-once is worth more than 24 bytes.

**What the tests hold.** Mutual exclusion both ways, readers not excluding
readers, writer priority over new readers, the caller's context bounding both
waits, `TryRLock`/`TryLock` refusal, idempotent `Release`, and exclusion under
load. Two of those were mutation-tested rather than trusted: removing the
`waiting--` on a writer's context-bail path, and letting `RLock` admit a reader
past a waiting writer. Both are caught — the first by
`TestRWMutexAbandonedWriterDoesNotBlockReaders`, which is the test that matters
most because that accounting is what the whole type turns on.

The abandoned-writer test took three revisions. Its first version asserted with
`context.Background()`, so the mutation made it **hang** until the package
timeout instead of failing: a test that catches a bug by wedging is a worse
failure mode than one that reports it, and the fixture now carries a deadline
so the same mutation fails in five seconds with a message.

`analysis/lostrelease` found unreleased `TryLock`/`TryRLock` permits in the new
test file within a minute of the `//velocity:acquires` directives going in, on
branches where `t.Fatal` means the release never runs. That is the analyzer
working as intended on a package it had never seen, and the fix — release before
failing — is the right shape anyway.

## A dropped Shared.Clone was invisible, and the reason recorded for it was not a reason (implemented)

Found while answering a consumer's question about building a shared pawn
registry on `Shared`, which is the first `ownership` call site either consuming
project has found. Before recommending the type I checked whether a handle on it
is actually checked, and it is not:

- `Shared.Clone` and `Frozen.Clone` return releasable counted handles and
  neither carried a `//velocity:acquires` directive.
- The drift test could not catch it either, because both sat in its
  `notAcquirers` table with the reason **"counted handle from one already
  held"**.

The reason is the bug. A `Shared` is reference-counted, so a clone is not a
transfer of an existing obligation — it is a *second* one. `Clone` increments
the count, and the cell's `Drop` runs when the count reaches zero, so a dropped
clone is a resource that is never released, which is the entire class
`lostrelease` exists to catch. The exclusion appears to have read "the
obligation already existed" as "so a further one need not be reported", which
is true of a move and not of a counted handle.

Verified rather than inferred, in a scratch module against a local replace:
dropping a `Shared.Borrow` is reported, and dropping a `Shared.Clone` beside it
in the same function is not. The incoherence is the argument — the same type
checks one of its two ways of handing out a handle and ignores the other.

**The fix, and what it costs.** A `//velocity:acquires` directive on each
`Clone`, both added to the fallback table for pre-marker versions, and the two
exclusions removed. Measured before committing: the drift test passes, the
analyzer produces **no new reports over velocity's own tree** under the stricter
policy, and the dropped-clone case is reported. So the check is free to the
library and catches the thing it was always supposed to.

**Why it is worth an entry rather than a commit message.** It was found by
reading the API in order to advise a consumer, not by a failing test — the
drift test was passing, the analyzer was passing, and velocity's suite was
green throughout. A tool whose subject is "the failure mode is forgetting"
had a hole in exactly the place a refcount makes forgetting expensive, and the
hole was documented with a rationale that reads like a considered trade. That is
the kind of entry that only gets written down if it is written down: a green
suite is evidence about the checks that exist, not about the checks that do not.

## Two cancellation rules that look contradictory and are not (documented)

Checking whether the `OnComplete` gap in `ErrGroup` — a submission that never
ran reporting nothing — also repeated in its sibling `dedupe`. It does not, and
the reason is worth more than the check.

`dedupe`'s `OnComplete` fires from a `defer` inside `run`, and `run` is
launched unconditionally by whichever caller becomes the leader, so a round
always executes its callback and always reports. `ErrGroup` needed the fix
because it has a submission that is accepted and then not run. The difference
is structural: a group has a path between "submitted" and "running", and
`dedupe` does not.

That raised the question the check was meant to settle, and the answer is that
the two packages make **opposite decisions on the same situation**, on purpose:

- `ErrGroup.Go` **refuses** to run a function after the group is cancelled.
- `dedupe.Do` **runs** its callback even when the round's context is cancelled.

It is worth being precise about the case, because it is narrower than it first
looks: `dedupe` refuses to *start* a round on a finished context — `check`
returns `ctx.Err()` — so the divergence only arises once a round is under way
and every caller has abandoned it, which cancels the execution under an
already-launched callback.

The reason is what the work is for. A group function is an **independent work
item**: one that would run against a dead context produces a result nobody
asked for, so refusing it is free. A `dedupe` callback is **shared work** — one
execution serving every caller on the key. Skipping it strands every caller
that arrives afterwards: they join the existing call, find no value and no
error but `ErrCallbackExit`, and retry a key that can now never succeed. That
is the rule the package is built on — *a callback that ignored its cancellation
did the work, so it is not repeated* — and skipping would invert it.

So neither was changed. What was missing is that a reader who knows one package
would reasonably assume the other matches, and the two behaviours are
indistinguishable from their names. Both sites now say so, and point at the
other. A guard for it is unnecessary: both behaviours are now pinned by tests,
this one by the argument above and the package's existing abandonment tests.

## opcodes and opruntime: the reckoning ownership got, and the answer is not the same (open)

`ownership` was kept with no call site, on a stated basis and with the cost
named. The same question applies to `opcodes` and `opruntime`, and the answer is
less comfortable, because the two situations are not the same kind of thing.

**The facts.** 79 lines of plain `Op`/`Instruction` data shapes, and 443 lines
of registry-and-dispatch. Between them they have **no users**: `opruntime`
imports `opcodes`, nothing imports `opruntime`, and `opcodes`'s only importers
are `opruntime`'s own tests. Neither appears in the README's field-use section
or anywhere in the benchmark suite. The entry above already states the only
justification on record — they "earn their keep only where the op set is
genuinely pluggable at runtime (a caller registers handlers, as `dedupe`/`async`
do not, but a future scripting/replay layer might)."

**Why `ownership`'s answer does not transfer.** The two are kept for different
reasons in kind, and the difference is not size. `ownership` is a foundational
concept: the class of bug it prevents — a resource released twice, not at all,
or after someone moved it — is a bug this library's consumers hit, and the
reason to keep it was a discipline, not a utility. `opruntime` is a **utility**:
a dispatch table with handlers registered at run time. A reader who needs one
can write the switch, and this record already carries the measurement that they
should — `Table.Dispatch` is ~28% slower than a hand-written switch for the
same 10-op shape. So the honest position is not "it is under-used" but "it is
slower than the alternative for the case everyone has, and only wins for a case
nobody has."

**And the asymmetry that decides most of it.** `ownership`'s absence would be a
bug class; nothing in this library or its consumers misbehaves because there is
no dispatch table. That means the cost side is real — 522 lines that only
exercise themselves, in a public API, in a package that is not advertised — and
the benefit side is a hypothesis with no date on it. A package with no caller
rots quietly, and the record is the only thing that will notice, which is the
same argument the `ownership` entry makes about itself. Here it is stronger,
because there is no prospective call site to keep the code honest against.

**The three positions, and what would settle it.**

  - **Remove both.** v0, no consumers, 522 lines, and the feature they exist
    for is speculative. Cheap to do, and the record keeps the reasoning if a
    scripting or replay layer ever arrives.
  - **Keep, marked as not-API.** The type exists, the benchmark stays, and
    godoc stops implying a commitment. Preserves the measurement, which is the
    part with value, and stops the package being a claim.
  - **Keep as is**, on the strength of "it is cheap and tested". Defensible, and
    the one that decays: nothing distinguishes this from a package nobody
    maintains on purpose.

The decision is open, and it is the author's rather than derivable from the
code — the same position the `ownership` entry ended at before the basis was
given. What would settle it is either a prospective user for a pluggable op
set, which makes the answer keep, or a decision that speculative API is not
something a v0 library carries, which makes the answer remove.

## opcodes and opruntime are kept: the op set is registered at run time (decided)

The reckoning above left this open and asked for the basis, because nothing in
the code derives it. The basis is **fuzzing, and an op set a caller registers
rather than one the compiler can see** — which is the case the earlier entry
named as the only thing that would earn these their keep, and it turns out to
be the case the repository is already in.

`ownership`'s model test maps a fuzz byte to an operation with `op % 16` and a
switch over sixteen cases. The `async` model target added alongside
`ForEachFuncs` maps one with `op % 5` and a switch over five. **Two model tests,
two hand-rolled op vocabularies, and neither imports `opcodes`** — a byte is
decoded into an operation by an expression written out twice, with nothing
shared, nothing reusable, and no way for a third model to be written without a
third expression. That is the duplication `opcodes`' data shapes and
`opruntime`'s handler table exist to remove, and it arrived in the same commit
that added the second model test.

So the justification is not the hypothetical "a future scripting/replay layer"
the earlier entry allowed for. It is a present duplication with a concrete
second instance, in a package the library is now growing into: two models, with
a third plausible as the packages accumulate.

**What this does not claim.** Neither existing model uses `opcodes` today, so
nothing has been rewritten and no duplication has been removed. The argument is
that the mechanism is already right for what the tests are becoming, not that a
migration has happened. The ~28% that a hand-written switch beats
`Table.Dispatch` still stands for a fixed op set, and a model's op set is fixed
at the call site, so **these two tests are still better off with their own
switches today.** What a runtime-registered op set buys is the third model, or
one model that registers a different handler set per iteration — cases where the
switch would have to become a table anyway.

**The cost still stands, unchanged.** 522 lines exercised only by each other
remains a cost, and the way this entry is true is the way the `ownership` entry
is true: a named use case with a visible second instance, rather than a
speculation. If the models are unified onto it, that entry can be rewritten with
the migration in it; if a third model arrives and still hand-rolls its
encoding, this was the wrong answer and the reckoning above should be reopened.

## ErrGroup.GoContext is GoCtx (decided)

Renamed. `GoContext` is `GoCtx` across the API, the tests, the benchmarks, the
fuzz model and this record.

The name is shorter and the doc comment that justifies it reads better with the
shorter name in it, which is the whole of the case. It is a pre-v1 rename on a
method two consumers have read and one is about to build against, so it was
done while that consumer had not started rather than after.

**The cost, which is a naming inconsistency this record should not pretend is
absent.** The rest of the package spells the suffix: `WaitContext`,
`Semaphore.Acquire(ctx)`, `RWMutex.Lock(ctx)`, `dedupe.WithBaseContext`. So
`GoCtx` now sits beside `WaitContext`, and the doc comment for one names the
other in the same sentence — the package has both spellings for the same idea.
Renaming `WaitContext` to `WaitCtx` would make the pair consistent and is a
larger break for a method with more call sites; leaving it makes `GoCtx` the
odd one. Neither is free, and the choice is the author's. Recorded here so the
next reader is not left to infer that one of the two is a typo.

## ownership is not a mutex, and a consumer found that the hard way (documented)

A consumer replaced a registry's `sync.RWMutex` with `Owner` borrows, ran the
tests, and reverted: **a conflicting `Mutate` reports `ErrConflict` at once and
does not wait.** Their existing concurrency test said it without needing a new
one — 24 concurrent adds of distinct names, all 24 landing under the mutex,
8 landing and 16 refused under `Owner`. Two simultaneous operator adds become
one add and one spurious failure.

This is deliberate, and stated in three places: `View`'s doc — "Concurrent
Views coexist; a Mutate meanwhile reports ErrConflict" — and twice in this
record, the second time as *"The no-wait invariant is untouched. It is the
design."* The reason is in the `Seal`/`Drained` entry: this package has no
channels, selects or context waits anywhere, **and that absence is why a borrow
here cannot deadlock**. A wait inside a cell is how a cycle gets one. So
`ErrConflict` is the mechanism the deadlock argument rests on, not an artefact of
nobody having asked for a blocking form.

**What the review here missed, and it is the whole finding.** The two halves of
the proposal were vetted separately — the borrow scope, the lifetime, the
analyzer coverage, the retirement sequence — and accepted. Admission semantics
were never on the table, and admission semantics decide it. A mutex's job is
**queueing**; this package's job is explicitly **not queueing**. They are not
the same primitive and the swap was never going to work, however correct every
other part of the plan was.

**The split already exists, one package over.** `async.Mutex` and
`async.Semaphore` admit by waiting, cancellably; `ownership` admits by
refusing and owns the lifetime. That is the design rather than a gap in it:
**waiting is a property of the work, not of the resource.** A registry that
needs exclusion takes a `async.Mutex`; a value whose lifetime must be checked
takes an `Owner`.

**Correction, from a consumer reading `async.RWMutex`'s doc: those two
properties were conflated here, and the composition is smaller than this entry
claimed.** It said a mutex for the critical section plus `Seal`/`Drained` for
the lifetime is "two primitives reproducing what one mutex already does". That
is true of *exclusion* and false of *drain*, and the difference decides which
primitive a consumer reaches for:

- **A goroutine parked on a lock cannot be told to exit.** That is the hazard
  `async.RWMutex`'s doc describes, and it is fixed by making the exclusion
  cancellable — `RLock(ctx)` and `Lock(ctx)` return when the context ends. One
  primitive, and it gives a capability `sync.RWMutex` does not have.
- **`Seal`/`Drained` gives a different thing**: a checkable postcondition that
  nothing is in flight when teardown finishes, and a place that refuses new
  work. It does not release a goroutine already waiting for a lock, because that
  goroutine is inside the mutex, not inside an ownership cell.

**They fail in opposite directions, which is the form worth keeping.**
Cancellable exclusion releases a goroutine already parked on a lock and gives
no postcondition. `Seal`/`Drained` gives a postcondition and releases no waiter,
because a goroutine waiting for a lock is inside the mutex and not inside an
ownership cell. So neither is a cheaper and a dearer version of one property,
and "reach for cancellable exclusion first" is true only in the sense that it is
one primitive and the other is not optional once the postcondition is wanted.
A drain that wants both needs both, and the earlier "two primitives" figure was
right after all — for a reason neither this entry nor the consumer who corrected
it had given. Retirement is worth layering when something needs the
postcondition, which is a stronger claim than "we are shutting down".

**What would settle it is a count, not an argument.** For a consumer weighing
cancellable exclusion, the question is how often a reader actually parks on the
lock, because a critical section that is a map lookup parks nobody and the
insurance is against a hazard that does not occur. `async.RWMutex` already has
the instrument for it: `TryRLock` returns false instead of waiting, so a caller
that probes on a sample of its calls and counts the refusals measures contention
without a production loop or a drain. Neither building on that count nor ruling
it out is justified without the number.

`Mutate`'s doc now says all of this, because the assumption it invites is the
one that cost a consumer a revert: the property reads as an absence rather than
as the design, and the fix for a caller who wants to retry is to say so in the
open with `resilience.Retry` and `ErrConflict`, rather than have the cell do it
quietly on their behalf.

## MutateAsync, and where the context went instead of onto the call (implemented)

`Owner.MutateAsync(ctx, fn) *traits.Future[R]` is `Mutate` with the callback
moved to its own goroutine and **admission queued rather than refused**. The
Future is pending, or carries the callback's outcome. *(The first version of
this took no context and refused on conflict; both are corrected below.)*

**Admission queues rather than refusing — corrected, and the original reasoning
for refusing was wrong in a way that decided the design.** The first version
took the borrow synchronously and reported `ErrConflict` at the call, on the
argument that a goroutine waiting for the borrow "puts a wait inside the cell, and
a cell with waiters is how a cycle gets one". Two things were wrong with that.
The cell does not need to hold the wait: a `changed` broadcast,
closed-and-replaced whenever a borrow ends or the cell is sealed, gives every
waiter a chance and holds nothing, so **the package still contains no blocking
operation** and the invariant survives intact. And citing "the caller can write a
retry loop instead" argued for building this rather than against it — the loop is
a real repeated need, and `Mutate`'s own doc tells every contended call site to
write one.

So `MutateAsync(ctx, fn)` waits for the write borrow, and a contended mutation
is delayed rather than dropped. Two hundred submitted at once all land, and never
more than one callback runs at a time.

**The broadcast is deliberately unordered.** An ordered waiter list would be a
queue the cell owns, and a re-entrant caller would queue behind itself with
nothing timing it out — the deadlock this shape exists to avoid. A broadcast has
no ordering and no fairness, but it holds nothing in the cell and degrades to a
retry. Fairness is a question for a measurement, not a default.

**`Mutate` itself is unchanged and still refuses at once.** If the synchronous
path queued too, every borrow would wait and the no-wait invariant would go
with it, so the choice is the caller's: `Mutate` to be told immediately,
`MutateAsync` to wait for a turn.

**The cost, corrected by measuring each form rather than asserting it.** The
first version of this entry said re-entering the cell from a callback hangs, and
it does not. `View`, `Mutate` and `BorrowMut` called from inside the callback are
all turned away **at once** with `ErrConflict`, exactly as they would be from any
other goroutine — the callback holds the write borrow, and saying so beats
waiting. Detection was never needed for the common case; the cell already reports
it.

Exactly one shape waits: a **nested `MutateAsync` whose Future the callback
awaits**. The nested mutation queues, and the callback blocks on it while holding
the borrow that one needs, so the two wait on each other. That is the caller
choosing to wait inside a critical section it holds — the same mistake as
waiting on a `WaitGroup` from inside the work it guards — and not a failure to
detect anything. A deadline on the context turns it into an error at the point of
the wait, which is the reason `ctx` bounds the admission wait and not only the
caller's patience. All four forms are tests
(`TestMutateAsyncReentryByForm`), so the claim is checked rather than trusted:
three error, one waits.

**The context is back on the call, for a different reason than the first version
gave.** It was moved to `Await` on the argument that a context there could only
catch an already-done context, since the callback takes none. With a queue it
bounds the *admission* wait: a caller that gives up cancels the mutation before
it starts rather than leaving it queued. It still cannot cancel a callback
already running, so a Future dropped mid-callback is completed and released.

**The context is on `Await`, not on the submission.** A context on
`MutateAsync` could only do one thing: catch an already-done context before
admitting, because `fn` takes no context and so there is nothing for it to
propagate into. That is a real check and a near-vacuous one, and it would have
required a new `ErrNilContext` sentinel in a package that has no notion of
contexts at all. Bounding the *wait* is the one job a context can actually do
here, and `Await` is where the caller can act on it, so that is where it went.
If cancellation ever needs to reach the work, `fn` has to take a context, which
is a different API and a larger decision than this.

**Giving up on the wait does not give up on the work.** `Await` returning the
context's cause leaves the mutation running; it still completes and still
releases its borrow, and the Future still resolves for anyone watching. That is
what makes a Future droppable without thought: there is nothing on the caller's
path that has to happen, so there is nothing to forget. `Result` is the
non-blocking read and says so, and `ErrPending` is a distinct error rather than
a bool so it goes straight to `errors.Is`.

**One place this is more forgiving than the package's own rule.** Callbacks
must not panic — a rule that exists because a panic leaves state inconsistent.
Here a panic would have no caller to reach, so a goroutine panic would take the
process down. It is recovered into `*Panic`, carrying the value and the stack
and unwrapping to the original error, mirroring `async.Panic`. The borrow is
released **before** the panic is converted, so a panicking callback cannot wedge
the cell — which is the failure the rule was written to prevent, and which the
test asserts directly.

Nine tests, including the three the design lives or dies by: a conflict is
refused synchronously rather than on the Future, fifty dropped Futures leave the
cell admitting, and a panicking callback both reports and leaves the cell usable.

## traits.Result and traits.Future, and a correction to the reason for migrating dedupe (implemented)

Two types in `traits`, the package that already holds the vocabulary shared
across this repository — `Drop`, `Clone` — and nothing else: a Result is the
outcome of work, a Future is a handle on work that has not finished. They are
separate because **"not finished" is a property of the handle, not of the
outcome**, so an unresolved Future has no Result rather than a Result holding a
sentinel meaning "wait".

The reason to split them is a footgun the combined form has. A Future whose
`Result()` returned `(R, error)` and reported *pending* in the error channel
means a caller writing `if err != nil` treats "not ready" as "failed". The
sentinel makes them separable by `errors.Is`, which is the same
convention-instead-of-structure the rest of this repository argues against. So:

  - `Try() (Result[R], bool)` — the second return is *readiness*, not an error.
    Unknown, succeeded and failed are three different answers.
  - `Await(ctx) (Result[R], error)` — and the returned error is the **wait's**,
    while `Result.Err` is the **work's**. Giving up on the wait says something
    about the caller's patience and nothing about the work, which still runs
    and still resolves the Future for anyone else watching. That is what makes a
    Future droppable: nothing is abandoned, so nothing needs cleaning up.

**Correction: the reason given for migrating `dedupe.Result` was wrong.** It
claimed the zero `Result` was ambiguous — "indistinguishable from a key whose
value genuinely is the zero value", the same absent-versus-empty defect the
evaluation measured in watermill's `Metadata.Get`. Checked, and every
assignment in `DoBatch` sets either `Err` or `Value`, and the map is aligned to
the requested keys, so **there is no absent state and the zero `Result` is
unreachable.** No bug, and the migration is vocabulary consolidation: one
`traits.Result` that `dedupe.Result` is an alias of, so the outcome of one key
and the outcome of a mutation are the same type with nothing to keep in step.
An alias rather than a removal, so `DoBatch`'s return is unchanged for callers.

The zero `Result` is a **succeeded zero** and is documented as one, which is the
same reasoning: a Result has no "absent" state to be confused with, because a
Future that has not resolved has no Result at all.

Nine ownership tests and seven traits tests. The three the designs live or die
by: a conflicting mutation is refused at the call rather than on the Future,
fifty dropped Futures leave the cell admitting, and a panicking callback both
reports and leaves the cell usable — the last because the borrow is released
*before* the panic is converted, which is the failure the no-panic-callbacks
rule exists to prevent.

The earlier version of this entry recorded `Await` as returning only the wait's
error, and the author revised it: a caller writing the ordinary `if err != nil`
after a callback failed would have seen success, which is the footgun the split
was meant to remove. Reporting the work's failure too closes it without
collapsing the two facts — the wrap preserves `errors.Is` and `errors.As`
against the original, so a typed failure like `*Panic` or `ErrConflict` is
still reachable.

## velocityvet's own dead-end hint, found by a consumer following it (implemented)

A consumer trying to run the analyzer directly hit two dead ends, because both
suggestions are wrong:

    $ go tool velocityvet ./...
    velocityvet: invoking "go tool velocityvet" directly is unsupported; use "go velocityvet"
    $ go velocityvet ./...
    go velocityvet: unknown command

The message is **unitchecker's**, templated on the tool's name: it means "go
tool vet" and the substitution produced "go velocityvet", which is not a Go
subcommand. The working form is the two-line `go build` plus
`go vet -vettool=`, and it is the only one that works — a direct invocation
gets the analyzer no configuration, so it reports nothing or misreports.

`cmd/velocityvet` now detects the case and prints the form that works. The
detection is the one `unitchecker` itself uses, loosened slightly: `go vet
-vettool=` passes a single `*.cfg` path, and anything else is a user running
the binary. It is deliberately looser than unitchecker's exact check so a
legitimate vet run is never silenced — printed under `go vet` the hint would
appear once per analysed package and bury the findings, which is worse than no
hint at all. Both directions are tested.

`go velocityvet` cannot be intercepted from inside the program — `go` fails
before the binary runs — so the two documented forms carry that part.

This is the first defect a consumer has reported in velocity's own tooling, and
it is worth recording how: the analyzer, its tests and the whole suite were
green throughout. Nothing in this repository could see it, because the failure
is in the path a user takes *toward* the tool rather than in anything the tool
does.

## dedupe.Result is gone rather than aliased, and Scope stays out of the analyzer table (decided)

Two things a consumer's report forced, in opposite directions.

**The alias is removed.** `dedupe.Result[V]` had been made an alias for
`traits.Result[V]` so the two would not drift apart. Removing it is the better
answer for a type this new, and it was decided rather than assumed: `DoBatch`
now returns `map[K]traits.Result[V]` and there is one name for an outcome. That
is a break against `v0.6.0`, which shipped the alias hours earlier, and no
consumer had adopted it in between.

**`Scope` is deliberately not in the `lostrelease` acquirer table, and the
reason is that it is not the same kind of thing.** The table holds handle-returning
calls whose result a caller must hand back — a borrow, a permit, a checkout. A
`Scope` has no `Release`; cleanup on it is explicit, and a scope that is never
closed is a deliberate no-op rather than a lost handle. The package says so:
cleanup is explicit only, and a `Drained` that never closes is the documented
outcome.

So a dropped `Scope` **is** a leak — the resources enrolled on it are never
released — but it is a leak of a lifetime rather than of a handle, and the
analyzer's model cannot express it by adding a row. Reporting the table's
coverage as though it included scopes would be worse than the gap, because
someone would rely on a check that is not there. Left out, deliberately, and
recorded here so the exclusion reads as a decision.

The consumer's phrasing was the right one and is worth keeping: they migrated
fifteen-odd `Close` sites, declined the rest with reasons, and explicitly
reported the migration as clean on the strength of their lock test rather than
on the strength of `lostrelease` — which proved nothing about it. A migration
reported clean by a checker that never looked is a claim about nothing.

## The //lint:ignore directives in this repository suppress nothing (corrected)

Found while adding a third nil-context test and watching a directive I had
written — in the same shape as two that work — do nothing.

**The two that work are suppressed by `.golangci.yml`, not by the directive.**
The `SA1012` rule in that file enumerates the test files that pass a nil context
on purpose, and an earlier version of its comment credited the directives. It
had been crediting them all along. Standalone `staticcheck`, which `just vet`
runs, does honour `//lint:ignore`; the copy inside `golangci-lint` does not, so
both the directive and the exclusion are needed and **only the exclusion is
doing anything in CI**.

Three things went wrong before that was found, and each is a way of being
confident from resemblance:

  - the directive was placed above an `if` whose statement spanned two lines, and
    the diagnostic is reported on the argument inside it rather than on the
    statement;
  - a `//` separator line was added between the prose and the directive, which
    looks like separation and is not — a comment group is broken by a blank line,
    not by another comment marker;
  - the assignment was then moved onto one line with the directive directly above
    it, matching the working cases exactly, and it still did not suppress,
    because the working cases were never working for the reason they appeared to.

The third is the one worth keeping. The fix was to read `resilience`'s
suppressed line, notice its file appears in the config's exclusion, and conclude
that the mechanism I had been copying was inert. **A working example is evidence
about the output, not about the mechanism**, and the cheapest way to tell them
apart is to change the input.

The directive stays in the three files, because the standalone checker in the
justfile does honour it, and the two checkers genuinely disagree. The config now
names `ownership/async_test.go` alongside the other two, and says which one is
doing the work.

## The unlock paths have no floor, and a double release breaks exclusion silently (implemented)

Proposed by the author as a way to spend the `-tags=velocitydebug` split, after
a consumer found a live dependency on the guard a proposed optimisation would
have removed.

`Permit.Release` is guarded by a `sync.Once`, so a second call is a no-op. That
absorbs the ordinary double release — a `defer` beside an explicit call, which is
the documented pattern. A consumer asked for the `Once` to become a plain `bool`
to halve the permit from 48 to 24 bytes, and **the proposal was withdrawn on
their evidence**: they have a test helper that returns a bare `permit.Release()`
and three call sites that release both explicitly and through a defer, which is
safe only because of the guard.

What that exposed is the part worth keeping. `unlockRead` is `readers--` with no
floor:

	m.readers--
	if m.readers == 0 { m.wake() }

A double release drives the count **negative**, which makes `readers == 0`
unreachable. `wake` never fires, so waiting readers never re-read the state, and
a writer is admitted while readers still hold the lock. **That is a silent break
of the exclusion this type exists to provide** — not a leaked count — and it
would appear in whatever runs next, with the release that caused it several calls
away. The copy route is closed independently: `q := *p` on a `*Permit` is a
`copylocks` vet error, because the permit contains a `sync.Once`.

**The `Once` absorbs; it does not detect.** So swapping it for a `bool` in
release builds would not add a diagnostic, it would convert silent absorption into
silent corruption — and debug would pass exactly where release breaks, which is
backwards for a net. That is the argument against the build split as first
proposed, and it is why the tag is spent on a floor assertion instead.

`unlockRead` and `unlockWrite` now call `checkReadRelease` and
`checkWriteRelease`, which panic under `-tags=velocitydebug` and inline away
otherwise, following `ownership`'s existing `leak_debug.go` / `leak_nodebug.go`
pair. A legitimate release trips neither, which is asserted so the net is not
just noise. The test reaches the counters directly rather than through
`Release`, because `Release` is precisely what the `Once` makes un-trippable —
that is the division of labour between the two, and the test has to honour it.

**The size change is not taken.** 48 to 24 bytes and about 5 ns of a 26 ns
allocation is real and is worth nothing at the one measured workload: a registry
taking 19 reads a minute pays about a microsecond a minute. Removing the guard
from that path would trade a load-bearing safety property for a rounding error.

## The README was a day behind, and a table row pointed at a section that did not exist

Found while checking whether `main` and the latest tag disagree — which they do,
about `MutateAsync` — and looking for the same kind of staleness elsewhere.

Four things, and three of them are the failure this record keeps finding in
another form: **a claim about a thing, written from a reading rather than from a
check.**

- `go get github.com/apsis-io/velocity@v0.4.0`, two minor versions stale.
- The field-use section put Periapsis on `v0.4.0` and breeze on
  `v0.5.0-rc.1`. Both pin `v0.5.1`. It also claimed a version was *deployed*,
  which is a fact this repository cannot check — the pin is visible in a go.mod,
  what runs on a cluster is not — so that claim is now stated as the pin, with
  the rest marked as not-knowable-from-here.
- The `ownership` section said a conflicting access is "never waited out, so
  nothing in the package can deadlock", which stopped being true when
  `MutateAsync` started queueing. It now says what is true: `View` and `Mutate`
  never wait.
- A table row added during that same pass linked to `#traits`, and **no `traits`
  section existed** — a new anchor pointing at nothing, added while fixing the
  other stale claims.

None of this would fail a test, a build, or a linter, which is the point. The
public face of the library described a library that had stopped being accurate
somewhere around `ErrGaveUp`, and the only way it was found was by looking for
the one thing already known to be wrong and asking what else was in the same
category. **A grep for the known-stale claim is a search for its siblings**, and
the first sibling found was a broken link introduced in the fixing.

## The exported surface was carrying three things that no longer exist (implemented)

Stopping feature work to solidify the API, and looking at the whole surface with
`go doc` rather than at the diffs that produced it, found three public names
describing things the record says were deleted or never were.

**`Plan` is gone; its vocabulary was not.** The DX pass removed it — "it existed
to validate eagerly and copy tasks for reuse, and nothing reused one" — and there
is no `type Plan` or `NewPlan` left. But `async` still exported `PlanError`
("async plan: task N: ..."), `ErrInvalidPlan` ("invalid async plan") and
`ErrNoTasks` ("async plan has no tasks"). A caller whose `Gather` was handed a
nil function got a message about a type that cannot be constructed. Now
`TaskError` and `ErrInvalidTask`, with `ErrNoTasks` keeping its name and losing
"plan" from its message.

**`ErrNilRunner` was returned for receivers that are not runners.** Four of the
five sites were wrong on their face: a nil `*Semaphore`, a nil `*Mutex` and two
nil `*RWMutex` all reported "nil async runner". Someone with a nil `*Semaphore`
is told the runner is nil — a type they never touched, with total confidence.
`ErrNilReceiver` fits all five sites, because all five are nil receivers.

**`Panic` existed twice, identically.** `async.Panic` and `ownership.Panic`, both
`{Value any; Stack []byte}`, one reporting "panic: ..." and the other
"ownership: panic in mutation callback: ...". A caller recovering a panic from
either had to handle both, and `errors.As` against one missed the other. One
`traits.Panic` now, which is where shared vocabulary already lives, and the two
names are gone rather than aliased — an alias would have left the duplication
visible under a name that implies it is not duplicated.

The cost of the last one is a prefix: `ownership.Panic` said which callback
panicked, and the shared type says "panic: ...". Losing the package-specific
wording is the price of one type, and it is the right trade — a panic carrying
its own stack does not need the package name to be diagnosable.

**And the behaviour question that sat beside it, now answered separately.**
`Gather(ctx)` over zero tasks used to return `ErrNoTasks`, which was right when an
empty `Plan` was a configuration error and is unfriendly for a v1 API: a fan-out
over nothing did nothing, and that is not a failure.

A fan-out that *collects* results now returns an empty slice and a nil error —
`Gather`, `GatherFuncs`, `Map`, `ForEach` and `ForEachFuncs`. **`Race` and
`FirstSuccess` still refuse an empty set**, and the distinction is not
conservatism: a race with no contenders has no winner, so the zero `Outcome` would
be claiming that the zero value won. `Race` therefore keeps the check, with the
reason written where the check is.

It was taken as its own change rather than folded into the rename above, because a
behaviour change inside a rename commit is the one a reviewer has to notice, and
this one is not obvious from the diff.
