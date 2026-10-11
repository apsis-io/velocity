# Upstream and rebasing

This package is vendored from the apsis-io/nsync fork of
github.com/vburenin/nsync (MIT; see LICENSE). The snapshot's upstream
base is vburenin/nsync master @ 4546563 — the last commit that is
upstream's; everything in the fork after that is local.

## The local delta, by file

- `plainmutex.go` — ours entirely: Mutex, the zero-value-valid
  inline-state mutex with cache-line-padded state.
- `nocopy.go` — ours entirely.
- `mutex.go` — two patched functions inside upstream's file: park's
  granted branch checks the starving bit before reading the clock, and
  unlock/unlockSlow decrement with one Add and undo a negative word
  before panicking. Everything else in the file is upstream's.
- `trymutex.go`, `semaphore.go`, `cacheline_*.go`, `spin_*.go` —
  upstream, unmodified.
- `waitqueue.go` — upstream minus waitList.grantAll, whose only
  callers were the dropped types.
- The test files are upstream's, trimmed of the types this copy does
  not carry (named locks, flags, the controlled wait group), plus a
  local cache-line layout check for Mutex.

## Rebasing on upstream movement

1. Fetch upstream and diff the four files above against 4546563:
   `git -C <fork-clone> diff 4546563..<new> -- mutex.go trymutex.go semaphore.go waitqueue.go`
2. Port the delta into this package by hand. The conflict surface is
   the two patched functions in mutex.go — apply upstream's fixes
   around them. The other three files usually apply verbatim.
3. Re-run this package's tests (they are upstream's own, trimmed) and
   velocity's full battery. The mutual-exclusion and invariant tests
   have already rejected one wrong port; trust them.

## Releasing

Nothing here changes velocity's version: the code ships inside
velocity's own releases, and the go.mod carries no nsync requirement.
The fork repository is archived; released velocity tags before the
dissolve still resolve their pins through it.
