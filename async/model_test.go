package async_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/apsis-io/velocity/async"
)

// FuzzErrGroupModel checks invariants of ErrGroup that must hold for every
// interleaving, rather than any particular schedule. It is the randomised
// counterpart to the hand-written tests, and it exists because the package
// people actually use had none: the repository's one fuzz target covers
// ownership, which no consumer has found a site for.
//
// Every invariant here is a statement about counts. None is a statement about
// timing, because a bound derived from idealised arithmetic is the thing that
// makes concurrency tests lie:
//
//   - no more than Limit functions run at once, through any submission path;
//   - a function runs at most once;
//   - a function submitted through GoContext with a context that was already
//     done never runs at all, whatever else is in flight;
//   - a submission after the group has finished returns, which it can only do
//     by taking a permit. Every function having returned and this both
//     completing is what a released permit looks like from outside.
//
// That last one has a bound, and it is deliberately loose. A leaked permit
// blocks a submission forever rather than briefly, so five seconds is not a
// measurement — it is long enough that no loaded machine fails it, and short
// enough that a real leak is reported rather than hanging the suite.
//
// The first error reported by Wait is deliberately NOT asserted here. It is
// whichever failure completes first, not the lowest submission index, so the
// property is about schedule and a model test has no business asserting it.
// submission order is Errors(), and the hand-written tests cover both.
func FuzzErrGroupModel(f *testing.F) {
	// The three submission paths and failing functions, mixed in one sequence.
	f.Add([]byte{0, 1, 2, 3, 4, 5})
	f.Add([]byte{10, 0, 11, 1, 10, 2})
	f.Add([]byte{8, 9, 0, 1, 6, 7})
	f.Add([]byte{6, 7, 8, 9, 10, 10, 10})
	f.Add([]byte{10, 10, 10, 10})
	// An empty sequence is a real input, not a degenerate one: the limit is
	// taken from the first byte, so a fuzzer that generates one finds the
	// indexing long before anything else.
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, ops []byte) {
		if len(ops) == 0 {
			return
		}
		limit := 1 + int(ops[0])%4
		run, err := async.New(async.Limited(limit))
		if err != nil {
			t.Fatal(err)
		}

		var (
			mu      sync.Mutex
			runs    = map[int]int{}
			refused = map[int]bool{} // submitted against an already-done context
			running atomic.Int64
			peak    atomic.Int64
			next    int
		)

		eg, _ := run.ErrGroup(context.Background())
		done, cancelDone := context.WithCancel(context.Background())
		cancelDone()

		submit := func(op byte) {
			id := next
			next++

			fn := func(ctx context.Context) error {
				// Count the run before anything else, so a second run is
				// caught even if that run then fails.
				mu.Lock()
				runs[id]++
				mu.Unlock()

				n := running.Add(1)
				for {
					old := peak.Load()
					if n <= old || peak.CompareAndSwap(old, n) {
						break
					}
				}
				defer running.Add(-1)

				// A function that ends because its context did is not a
				// failure: the group is not failed by its own cancellation.
				if err := ctx.Err(); err != nil {
					return nil
				}
				if op%3 == 0 {
					return errors.New("modeled failure")
				}
				return nil
			}

			switch op % 5 {
			case 0, 1:
				eg.Go(fn)
			case 2:
				eg.TryGo(fn)
			case 3:
				refused[id] = true
				eg.GoContext(done, fn)
			case 4:
				eg.GoContext(context.Background(), fn)
			}
		}

		for _, op := range ops[1:] {
			submit(op)
		}

		_ = eg.Wait()
		cancelDone()

		if p := int(peak.Load()); p > limit {
			t.Fatalf("%d functions ran at once, limit is %d", p, limit)
		}

		mu.Lock()
		for id, count := range runs {
			if count > 1 {
				t.Fatalf("function %d ran %d times", id, count)
			}
			if refused[id] && count > 0 {
				t.Fatalf("function %d ran, but its context was already done", id)
			}
		}
		mu.Unlock()

		// Go takes a permit before it checks the group context, so it can only
		// return if a permit was free. Every function has returned and the
		// group has finished, so a permit still held here is a leak.
		returned := make(chan struct{})
		go func() {
			eg.Go(func(context.Context) error { return nil })
			close(returned)
		}()
		select {
		case <-returned:
		case <-time.After(5 * time.Second):
			t.Fatal("a submission after the group finished never returned: a permit was not released")
		}
	})
}
