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
| **velocity** `dedupe.Do` | yes | yes | `*ownership.Shared[V]` handle, released per caller |
| **janos** `resenje.org/singleflight` | yes | yes | bare value |
| **samber** `go-singleflightx` | yes | **no** | bare value |
| **x/sync** `singleflight` | **no** — boxes through `any` | **no** | bare value |

| | result ordering | concurrency limit | error handling |
|---|---|---|---|
| **velocity** `async.Gather` | source index, `Outcome` with label | `Limited(n)` / `Unlimited` | `errors.Join` of all |
| **hunch** `All` | source index, via post-hoc sort | none | first error |
| **errgroup** | caller writes by index | `SetLimit` (unused here) | first error |

Fairness rules the benchmarks follow, so the deltas mean something:

- Every arm's callback does **identical** trivial work.
- velocity's `Release()` is **inside** the measured loop. It is required work;
  omitting it would flatter velocity.
- `b.ReportAllocs()` everywhere — x/sync's `any` boxing and velocity's handle
  allocation both belong in the comparison, not in a footnote.

## Results

Indicative only — measured on one machine, not authoritative.

```
Intel(R) Xeon(R) CPU E5-2690 v4 @ 2.60GHz · linux/amd64 · go1.27.0
hunch v1.1.3 · go-singleflightx v0.3.2 · resenje.org/singleflight v0.4.3 · x/sync v0.22.0
median of -count=5
```

**Dedupe, uncontended** (sequential, one key)

| | ns/op | B/op | allocs/op |
|---|---|---|---|
| x/sync | 167 | 80 | 1 |
| samber | 173 | 80 | 1 |
| janos | 1044 | 336 | 6 |
| velocity | 1551 | 584 | 10 |

**Dedupe, contended** (`RunParallel`, one shared key)

| | ns/op | B/op | allocs/op |
|---|---|---|---|
| x/sync | 277 | 75 | 0 |
| samber | 283 | 75 | 0 |
| janos | 348 | 12 | 0 |
| velocity | 1080 | 429 | 7 |

**Async gather** (8 trivial tasks)

| | ns/op | B/op | allocs/op |
|---|---|---|---|
| errgroup | 3482 | 856 | 35 |
| velocity `Unlimited` | 4579 | 1752 | 19 |
| velocity `Limited(4)` | 6559 | 1864 | 20 |
| hunch | 8122 | 1960 | 34 |

**dedupe backends**, all three workloads (ns/op, median of 5)

| | uncontended | one shared key | key per goroutine |
|---|---|---|---|
| `WithXsyncBackend` (default) | 1548 | **1047** | **596** |
| `WithMutexBackend` | **1428** | 1158 | 1134 |
| `WithSharded(8)` | 1559 | 1176 | 613 |

Allocations are flat per backend across all three: mutex and sharded 9 (560 B)
uncontended, xsync 10 (584 B).

## What the numbers mean

**velocity is the slowest dedupe arm, and most of the gap is not ours.** The
clean controlled comparison is janos vs samber: both generic, both returning
bare values, differing only in context support — 1058 ns vs 171 ns. That ~890 ns
is the cost of *being cancellable at all*. samber and x/sync run the leader's
callback **inline on the caller's goroutine**; velocity and janos must run it on
a separate goroutine so a caller can abandon it, which means a goroutine spawn
and a `context.WithCancel` per call. They are not faster because they are better
written — they are faster because they cannot do this.

velocity's remaining ~500 ns over janos is the ownership handle: janos returns a
value and forgets it, velocity hands every caller an independently released
handle with Drop support. Whether that is worth it is a design question, and the
answer is workload-dependent — but the cost is real and it is measured here
rather than argued about.

Profiling this path drove several optimizations: a zero-option fast path in
`ownership.New`, `NewShared` to skip a throwaway `Owner`, and dropping the
borrow wrapper from scoped `Read`/`Write`. The remaining `Do` allocations are
load-bearing — the per-round `context.WithCancel` backs `Cancel` and
all-callers-left cancellation, and is invoked from four places — so this is the
floor for the current design, not an easy win left on the table.

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
