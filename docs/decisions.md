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

## Ownership (implemented)

- Concrete constructor-created Owner/Shared/access/borrow types use one
  concurrency-safe runtime state machine. No operation blocks for a borrow.
- Scoped Read/Write is the default vocabulary; Borrow/BorrowMut is the advanced
  explicit-handle vocabulary. Go 1.27 generic methods provide typed projection
  and update on concrete types and are not interface seams.
- Move creates a fresh Owner; Take exits the ownership system; IntoShared and
  sole-handle TryUnwrap provide explicit conversion. Shared cloning is explicit.
- Release and Close are aliases. Optional Drop is explicit, at most once, and
  never driven by GC. Optional Clone powers Snapshot.
- Normal callbacks, Clone, Drop, and future Observer callbacks must not panic or
  call Goexit. Errors and context causes are the supported failure paths.
- This is runtime borrow-state enforcement, not compile-time ownership, deep
  immutability, rollback, or alias revocation.

## Deferred observe

- Opt-in Observer interface with an allocation/timing-free disabled path.
- Lifecycle counters and transition bits coalesce in a keyed dirty-state map,
  not an event queue; event-driven batch delivery has a configurable one-second
  watchdog fallback.
- Detailed terminal state is removed after delivery while cumulative counters
  remain. Public stats include aggregates and copied active-operation iterators.
- Raw dedupe keys are hidden unless callers configure a safe label projection.

## Deferred dedupe

- Constructor-required group with required base context, caller-interest
  contexts, cancellation only after every caller leaves, and key retention until
  even non-cooperative work actually exits.
- Ordinary blocking Do plus typed reusable futures, explicit Release, Forget
  versus Cancel separation, aligned native batch results, missing-result errors,
  and constructor-selectable built-in mutex/xsync/sharded registries.
- A borrowed API has read and mutable forms. The leader loans an Owner input;
  acquisition conflicts fail before key registration, duplicate inputs remain
  untouched, the loan lasts for the generation, and releases before result R is
  published.
- Comparable keys follow ordinary Go map hazards. A sharded backend uses Go
  1.27 generic `hash/maphash` hashing.

## Deferred async and resilience

- `async` uses required positive limits or an explicit Unlimited value,
  completion-ordered Gather/Race/FirstSuccess and single-use iterator runs from
  immutable plans. Stable source index plus optional label permits order
  reconstruction. Take/Last are recipes, not APIs.
- Concrete fluent heterogeneous pipelines use Go 1.27 generic `Then` methods;
  stages fail fast under one run context with optional narrower stage contexts.
- `resilience` starts with explicit policy-driven Retry, classifiers,
  cancellation-aware backoff/jitter, and injectable clocks. Breakers/limiters
  are future composition work.
- Future operational goroutines belong to object-owned WaitGroup.Go lifecycles.
  Context-aware Close uses explicit active counters and a terminal channel, not
  accumulating waiter goroutines.
- Root Task/Outcome/ID representation and registry defaults remain benchmark
  decisions rather than committed API.
