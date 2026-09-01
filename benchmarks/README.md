# Comparison benchmarks

Head-to-head benchmarks against the libraries velocity's `dedupe` and `async`
took their ideas from. This is a separate Go module, so these do **not** run
under the root module's `go test ./...`:

```sh
cd benchmarks
go test ./... -run '^$' -bench . -benchmem -count=5
```

It depends on the parent via a `replace` directive, so it always measures the
working tree, not a published version.

## Read this before the numbers

The arms are not equivalent, and a table alone would be misleading. What each
one actually does:

| | generic | context / cancellable | result |
|---|---|---|---|
| **velocity** `dedupe.Do` | yes | yes | bare value |
| **velocity** `dedupe.DoShared` | yes | yes | `*ownership.Shared[V]` handle, released per caller |
| **janos** `resenje.org/singleflight` | yes | yes | bare value |
| **samber** `go-singleflightx` | yes | **no** | bare value |
| **x/sync** `singleflight` | **no** — boxes through `any` | **no** | bare value |

| | result ordering | concurrency limit | error handling |
|---|---|---|---|
| **velocity** `async.Gather` | source index, `Outcome` with label | `Limited(n)` / `Unlimited` | `errors.Join` of all |
| **hunch** `All` | source index, via post-hoc sort | none | first error |
| **errgroup** | caller writes by index | `SetLimit` (unused here) | first error |

| | first error cancels | limit | panics | all errors |
|---|---|---|---|---|
| **velocity** `async.ErrGroup` | yes, with cause | Runner's, bounds goroutines | recovered as error | `Errors()` |
| **x/sync** `errgroup` | yes, with cause | `SetLimit`, bounds goroutines | re-panic in `Wait` | first only |

| | per-item result | pool | cancellable |
|---|---|---|---|
| **velocity** `async.Map` | bare `R`; `*ItemError` per failure, joined | `Limited(n)` workers | yes, via `ctx` |
| **conc** `iter.Mapper.MapErr` | bare `R`, errors joined | `MaxGoroutines` workers | **no** |
| **errgroup** hand-rolled pool | bare `R`, first error | atomic-counter workers | no |

Fairness rules the benchmarks follow, so the deltas mean something:

- Every arm's callback does **identical** trivial work.
- `DoShared`'s `Release()` is **inside** the measured loop. It is required
  work; omitting it would flatter velocity.
- `b.ReportAllocs()` everywhere — x/sync's `any` boxing and velocity's handle
  allocation both belong in the comparison, not in a footnote.

## Results

Indicative only — measured on one machine, not authoritative.

```
Intel(R) Xeon(R) CPU E5-2690 v4 @ 2.60GHz · linux/amd64 · go1.27.0
hunch v1.1.3 · go-singleflightx v0.3.2 · resenje.org/singleflight v0.4.3 · x/sync v0.22.0 · conc v0.3.0
median of -count=5
```

**Dedupe, uncontended** (sequential, one key)

| | ns/op | B/op | allocs/op |
|---|---|---|---|
| x/sync | 170 | 80 | 1 |
| samber | 176 | 80 | 1 |
| janos | 1081 | 336 | 6 |
| velocity `Do` | 1303 | 440 | 6 |
| velocity `DoShared` | 1460 | 576 | 8 |

**Dedupe, contended** (`RunParallel`, one shared key)

| | ns/op | B/op | allocs/op |
|---|---|---|---|
| x/sync | 282 | 76 | 0 |
| samber | 284 | 76 | 0 |
| janos | 345 | 12 | 0 |
| velocity `Do` | 1002 | 340 | 4 |

**Async gather** (8 trivial tasks)

| | ns/op | B/op | allocs/op |
|---|---|---|---|
| errgroup | 3689 | 856 | 35 |
| velocity `Unlimited` | 4723 | 1880 | 19 |
| velocity `Limited(4)` | 6995 | 1992 | 20 |
| hunch | 8451 | 1960 | 34 |

**ErrGroup** (8 functions, first-error semantics)

| | unlimited ns/op | allocs | limit 4 ns/op | allocs |
|---|---|---|---|---|
| x/sync errgroup | 3206 | 20 | 4847 | 21 |
| velocity `ErrGroup` | 3386 | 12 | 4760 | 13 |

**Async map** (one function over a collection, 8 workers)

| | 8 items ns/op | 1024 items ns/op | B/op at 1024 | allocs/op |
|---|---|---|---|---|
| errgroup pool | 3304 | 28863 | 8992 | 20 |
| conc `MapErr` | 3257 | 33614 | 8592 | 16 |
| velocity `Map` | 4026 | 35502 | 9592 | 20 |

**dedupe backends**, all three workloads (ns/op, median of 5)

| | uncontended | one shared key | key per goroutine |
|---|---|---|---|
| `WithXsyncBackend` (default) | 1287 | **988** | **482** |
| `WithMutexBackend` | **1222** | 1091 | 1117 |
| `WithSharded(8)` | 1320 | 1124 | 593 |

Allocations are flat per backend across all three: mutex and sharded 5 (416 B)
uncontended, xsync 6 (440 B).

## What the numbers mean

**velocity is the slowest dedupe arm, and most of the gap is not ours.** The
clean controlled comparison is janos vs samber: both generic, both returning
bare values, differing only in context support — 1058 ns vs 171 ns. That ~890 ns
is the cost of *being cancellable at all*. samber and x/sync run the leader's
callback **inline on the caller's goroutine**; velocity and janos must run it on
a separate goroutine so a caller can abandon it, which means a goroutine spawn
and a `context.WithCancel` per call. They are not faster because they are better
written — they are faster because they cannot do this.

`Do` is now ~220 ns over janos at the same six allocations: the round's `call`,
its `done` channel, the `context.WithCancel` that backs `Cancel` and
all-callers-left cancellation, the leader goroutine's closure, and the registry
entry. The ownership handle that used to be built into every round is now
opt-in: `DoShared` costs ~160 ns and two allocations more, and a group
configured with a result `Drop` pays a further ~150 ns for the per-round cell
that runs it once, after the last caller releases. An earlier design built that
cell on every `Do` whether or not anything would ever drop it; the comparison
made the price visible, and no consumer depended on the old shape.

**velocity beats hunch on async and loses to errgroup**, both for structural
reasons. hunch boxes every result through `interface{}` and restores source
order by sorting afterwards; velocity is generic and assigns into a pre-sized
slice by reserved index. errgroup wins because it does less — no `Outcome`
structs, no labels, no limit machinery, no error joining — and if you need none
of those, it is the right tool.

`Gather` used to derive a cancellable context it never cancelled (unlike
`Race`/`FirstSuccess`, which do use it to stop siblings) and allocated an error
slice even when nothing failed. Removing both took it from 23 to 19 allocations
and narrowed the gap to errgroup from 1.5x to 1.3x.

**`ErrGroup` matches errgroup within noise and allocates ~40% less**,
while also recovering panics, passing the context, skipping functions
submitted after failure, and collecting every error. Two things it took to
get there: `sync.WaitGroup.Add` + `go` rather than `WaitGroup.Go`, whose
wrapper closure is an allocation and an indirection per function; and a
plain permit send checked afterwards rather than a `select` against the group
context, which cost ~250 ns per contended permit and bought only an earlier
return for a submitter stuck behind functions that ignore cancellation —
which x/sync does not offer either.

**`Map` is within ~8% of conc at 1024 items, and cancellable where conc is
not.** All three arms dispatch the same way — a fixed pool pulling indices from
an atomic counter — and all three now write a bare `R` per item. An earlier
`Map` wrote a 48-byte `Outcome` per item so a caller could learn which items
failed, and trailed conc by 1.6x for it; failures are the rare case, so they
now travel out of band as one `*ItemError` per failure in the joined error,
and the success path touches only the result slot. Per-item clock reads for
`Hooks` are skipped when no hook is set. Against `Gather` over the same
collection the comparison is not close: `Map` is ~25x faster at 1024 items with
constant allocations, because `Gather` spawns one goroutine per task and `Map`
does not.

**`Limited(4)` costs ~50% over `Unlimited`** for 8 trivial tasks. The permit
channel is not free. With real task bodies that overhead is amortized away, but
bounding concurrency is not a no-op and should be a deliberate choice.

**The backend default is xsync, and the choice is workload-dependent.** xsync is
~1.9x mutex when many goroutines register distinct keys, and ~10% faster on a
single contended key — but it *loses* ~8% uncontended and costs one extra
allocation in every workload. xsync is the default because the payoff is
asymmetric: the win where it wins is much larger than the loss where it loses,
and code reaching for a dedup library usually has concurrency.

`WithMutexBackend` is not deprecated and remains the right choice for
low-concurrency or allocation-sensitive callers. `WithSharded(n)` sits between
the two and only pays off at high cardinality; it has no workload here where it
is the outright best, so prefer one of the other two unless you have measured
your own.

An earlier version of this benchmark measured **only** the key-per-goroutine
case, which is the single workload mutex is worst at. That would have justified
deprecating mutex on evidence that never looked at the case mutex wins. The
three-workload table above exists so that mistake is visible rather than
repeatable.

## Scope

`ownership` is not benchmarked against anything — it has no comparable library;
it is the novel piece. `traits` is not either: a comparison against
`enetx/g` and `fogfish/golem` was evaluated and rejected, with reasoning
recorded in [`../docs/decisions.md`](../docs/decisions.md).
