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
| x/sync | 169 | 80 | 1 |
| samber | 171 | 80 | 1 |
| janos | 1058 | 336 | 6 |
| velocity | 1494 | 560 | 9 |

**Dedupe, contended** (`RunParallel`, one shared key)

| | ns/op | B/op | allocs/op |
|---|---|---|---|
| x/sync | 291 | 75 | 0 |
| samber | 289 | 76 | 0 |
| janos | 361 | 11 | 0 |
| velocity | 1250 | 396 | 6 |

**Async gather** (8 trivial tasks)

| | ns/op | B/op | allocs/op |
|---|---|---|---|
| errgroup | 3565 | 856 | 35 |
| velocity `Unlimited` | 5330 | 1992 | 23 |
| velocity `Limited(4)` | 8028 | 2216 | 25 |
| hunch | 8690 | 1960 | 34 |

**dedupe backends** (`RunParallel`, one key per goroutine)

| | ns/op | B/op | allocs/op |
|---|---|---|---|
| `WithXsyncBackend` | 571 | 584 | 10 |
| `WithSharded(8)` | 645 | 560 | 9 |
| `WithMutexBackend` (default) | 1266 | 560 | 9 |

## What the numbers mean

**velocity is the slowest dedupe arm, and most of the gap is not ours.** The
clean controlled comparison is janos vs samber: both generic, both returning
bare values, differing only in context support — 1058 ns vs 171 ns. That ~890 ns
is the cost of *being cancellable at all*. samber and x/sync run the leader's
callback **inline on the caller's goroutine**; velocity and janos must run it on
a separate goroutine so a caller can abandon it, which means a goroutine spawn
and a `context.WithCancel` per call. They are not faster because they are better
written — they are faster because they cannot do this.

velocity's remaining ~440 ns / 3 allocs over janos is the ownership handle:
janos returns a value and forgets it, velocity hands every caller an
independently released handle with Drop support. Whether that is worth it is a
design question, and the answer is workload-dependent — but the cost is real and
it is measured here rather than argued about.

Profiling this path is what motivated the `New` zero-option fast path and
`NewShared`, which together took `Do` from 11 to 9 allocations. The wall-clock
gain (~30-100 ns) overlaps run-to-run noise; the goroutine and context setup
dominate, and no amount of handle tuning changes that.

**velocity beats hunch on async and loses to errgroup**, both for structural
reasons. hunch boxes every result through `interface{}` and restores source
order by sorting afterwards; velocity is generic and assigns into a pre-sized
slice by reserved index. errgroup wins because it does less — no `Outcome`
structs, no labels, no limit machinery, no error joining — and if you need none
of those, it is the right tool.

**`Limited(4)` costs ~50% over `Unlimited`** for 8 trivial tasks. The permit
channel is not free. With real task bodies that overhead is amortized away, but
bounding concurrency is not a no-op and should be a deliberate choice.

**`WithXsyncBackend` is ~2.2x the default mutex backend under high-cardinality
contention.** This is the workload the default is worst at: every goroutine on
its own key, so all the contention is on the registry and none of it is
deduplicated. `WithMutexBackend` remains a reasonable default for single-key or
low-concurrency use, where its lower fixed overhead wins. If you are deduping
many distinct keys from many goroutines, switch.

## Scope

`ownership` is not benchmarked against anything — it has no comparable library;
it is the novel piece. `traits` is not either: a comparison against
`enetx/g` and `fogfish/golem` was evaluated and rejected, with reasoning
recorded in [`../docs/decisions.md`](../docs/decisions.md).
