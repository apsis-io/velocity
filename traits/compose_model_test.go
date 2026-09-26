package traits_test

import (
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/apsis-io/velocity/traits"
)

// A node stands for an owned intermediate. Values are made unique by
// construction — each step stamps its own tag, used once per composition — so
// identity checks do not have to cope with two steps producing equal values,
// which is exactly the case that makes value-based assertions ambiguous.
type node struct {
	tag int
	acc int
}

// inputTag is outside every step's range, so a test can recognise the caller's
// own value wherever it turns up.
const inputTag = -1

// errCloneFails and errDropFails are distinct so a joined error can be attributed.
var (
	errCloneFails = errors.New("clone failed")
	errDropFails  = errors.New("drop failed")
)

// stepClones builds a pipeline from a byte string: each byte is a transform,
// and the step at failAt returns errCloneFails instead. A failAt outside the range
// means no step fails.
func stepClones(steps []byte, failAt int) []traits.Clone[*node] {
	out := make([]traits.Clone[*node], len(steps))

	for i, b := range steps {
		tag, offset, fails := i, int(b%5)-2, i == failAt

		out[i] = func(in *node) (*node, error) {
			if fails {
				return nil, errCloneFails
			}

			// Carries the input's accumulator forward as well as stamping a new
			// tag, so a step that cannot change the value still produces a
			// distinct one and the invariants below stay checkable.
			return &node{tag: tag, acc: in.acc + offset}, nil
		}
	}

	return out
}

// assertReleased checks the COUNT as well as saying which values, because a
// composition that released the wrong number can satisfy every other property
// in this file: the released values are all legitimately older than the result
// whether there are one of them or four.
func assertReleased(t *testing.T, released []*node, want int) {
	t.Helper()

	tags := make([]int, 0, len(released))
	for _, v := range released {
		tags = append(tags, v.tag)
	}

	if len(released) != want {
		t.Fatalf("released %d values %v, want %d", len(released), tags, want)
	}
}

// The four properties Drop.Clone promises, checked for every combination of
// where the clone fails and where the drop fails. A table rather than four
// hand-written cases because the interesting cells are the ones nobody thinks
// to write: a drop that fails on the LAST intermediate, and a clone that fails
// at index zero, where there is no intermediate to release at all.
func TestDropCloneAcrossEveryFailurePosition(t *testing.T) {
	steps := []byte{1, 1, 1}

	for failAt := range len(steps) + 1 {
		failAt := failAt - 1 // -1 is "nothing fails"

		for dropFail := range len(steps) + 1 {
			dropFail := dropFail - 1

			t.Run(fmt.Sprintf("clone fails at %d, drop fails at %d", failAt, dropFail), func(t *testing.T) {
				var released []*node

				// The drop is the RECEIVER of Drop.Clone — that is the API under
				// test — and it does both jobs at once: it records what it
				// releases, which is what properties 1-3 read, and it fails on
				// every call when dropFail >= 0, which is the failure this cell
				// names. One drop rather than a recording witness plus a separate
				// failing one, because a drop cannot see which pipeline position
				// the value it is releasing came from, so "fails at position N" is
				// not expressible — only "fails".
				//
				// The torn first draft of this file built a recording drop and
				// threw it away (_ = drop), which left properties 1-3 vacuous —
				// nothing ever populated `released` — and the drop's own error
				// unreachable in all sixteen cells.
				var drop traits.Drop[*node] = func(v *node) error {
					released = append(released, v)

					if dropFail >= 0 {
						return errDropFails
					}

					return nil
				}

				clone, err := drop.Clone(stepClones(steps, failAt)...)
				if err != nil {
					t.Fatal(err)
				}

				got, cloneErr := clone(&node{tag: inputTag, acc: 100})
				// Property one: the caller's value is never released, and neither
				// is the value returned. Both are the caller's to use.
				// `released` doubles as the witness list the properties read: it
				// holds every value the composition handed to the drop, which is
				// what properties 1-3 are claims about. The torn draft had a
				// separate `seen` fed by a witness drop that was never wired in.
				for _, v := range released {
					if v.tag == inputTag {
						t.Fatalf("the caller's own value was released: %+v", released)
					}
				}

				if got != nil && got.tag == inputTag {
					t.Fatal("the composition returned the caller's own value unchanged")
				}

				// Property two: nothing is released twice. A double release is
				// the use-after-free this package exists to prevent, and it is
				// invisible to every other assertion here.
				counts := map[*node]int{}
				for _, v := range released {
					counts[v]++
				}

				for v, n := range counts {
					if n > 1 {
						t.Fatalf("value %d was released %d times", v.tag, n)
					}
				}

				// Property three: every released value is strictly older than
				// the last one produced, so a composer cannot release something
				// it is about to hand back.
				if got != nil {
					for _, v := range released {
						if v.tag >= got.tag {
							t.Fatalf("released tag %d, which is not older than the result's %d", v.tag, got.tag)
						}
					}
				}

				// Property four: the error names what actually happened, and WHICH
				// failures are visible depends on the order of the two — which is
				// not obvious, and is not what the doc comment implies. Measured for
				// this three-step pipeline, with the drop failing on every call:
				//
				//	nothing configured    no error; both intermediates released
				//	drop fails, no clone  errDropFails alone, and the pipeline stops at the
				//	                    first ownership transition
				//	clone fails first    errCloneFails alone, nothing ever owned, so the
				//	(k=0)               drop never ran and nothing was released
				//	clone at k=1,        BOTH: the clone failure triggers the cleanup
				//	drop fails           drop, and that drop fails too — the doc's
				//	                    joined-errors sentence, and it needs owned=true
				//	clone at k=2,        errDropFails alone, and errCloneFails is ABSENT: the drop
				//	drop fails           failed at the earlier transition and stopped
				//	                    the pipeline before the second clone ever ran
				//
				// The last two are the same inputs one step apart and they disagree,
				// which is why both sentinels are checked rather than one. released
				// is counted in every cell too, which nothing did before: a
				// composition that released the wrong NUMBER of values satisfies
				// every other property in this test.
				switch {
				case failAt < 0 && dropFail < 0:
					if cloneErr != nil || got == nil {
						t.Fatalf("nothing was configured to fail, yet clone = (%v, %v)", got, cloneErr)
					}

					assertReleased(t, released, 2)
				case failAt < 0:
					if !errors.Is(cloneErr, errDropFails) || errors.Is(cloneErr, errCloneFails) {
						t.Fatalf("only a drop was configured to fail, so clone = %v, want the drop's error alone", cloneErr)
					}

					assertReleased(t, released, 2)
				case failAt == 0:
					// Nothing was ever owned, so the drop is unreachable in both
					// directions and the clone failure is the whole answer.
					if !errors.Is(cloneErr, errCloneFails) || errors.Is(cloneErr, errDropFails) {
						t.Fatalf("the first step failed before anything was owned, so clone = %v, want errCloneFails alone", cloneErr)
					}

					assertReleased(t, released, 0)
				case failAt == 1:
					if !errors.Is(cloneErr, errCloneFails) {
						t.Fatalf("clone = %v, want the clone's error", cloneErr)
					}

					// The joined-errors sentence, which holds only because the
					// failing step was not the first.
					if dropFail >= 0 && !errors.Is(cloneErr, errDropFails) {
						t.Fatalf("the cleanup drop failed too, so clone = %v, want both errors", cloneErr)
					}

					if dropFail < 0 && errors.Is(cloneErr, errDropFails) {
						t.Fatalf("no drop was configured to fail, so clone = %v should not carry one", cloneErr)
					}

					assertReleased(t, released, 1)
				case dropFail < 0:
					if !errors.Is(cloneErr, errCloneFails) || errors.Is(cloneErr, errDropFails) {
						t.Fatalf("only a clone was configured to fail, so clone = %v, want errCloneFails alone", cloneErr)
					}

					assertReleased(t, released, 2)
				default:
					// The drop failed at the transition BEFORE this step, so the
					// pipeline never got here and the clone's own error is absent.
					if !errors.Is(cloneErr, errDropFails) || errors.Is(cloneErr, errCloneFails) {
						t.Fatalf("the drop failed first, so clone = %v, want the drop's error alone", cloneErr)
					}

					assertReleased(t, released, 2)
				}

				// A composition that failed never hands back a value, or a caller
				// holding a released resource would have no way to tell.
				if cloneErr != nil && got != nil {
					t.Fatalf("a failed composition returned tag %d as well as %v", got.tag, cloneErr)
				}
			})
		}
	}
}

// The same four properties for a pipeline of any length and any failure
// position, where the table above would need a case per length. Fuzzed rather
// than enumerated because the properties are invariants — they hold for every
// input, so the fuzzer is looking for a counterexample rather than for coverage.
func FuzzDropCloneInvariants(f *testing.F) {
	f.Add([]byte{1, 1, 1}, 1, 0)
	f.Add([]byte{0, 0, 0, 0}, -1, 2)
	f.Add([]byte{4, 4, 4, 4, 4}, 0, 4)
	f.Add([]byte{2, 3, 1, 0, 4, 2}, 3, 1)

	f.Fuzz(func(t *testing.T, steps []byte, failAt, dropFailAt int) {
		if len(steps) == 0 {
			t.Skip("an empty composition is rejected at construction, not here")
		}

		// Normalised rather than rejected: the fuzzer's integers are arbitrary,
		// and "no step fails" has to be reachable as often as any position.
		if failAt < -1 {
			failAt = -1
		}

		if dropFailAt < -1 {
			dropFailAt = -1
		}

		if failAt >= len(steps) {
			failAt = -1
		}

		if dropFailAt >= len(steps) {
			dropFailAt = -1
		}

		var seen []*node

		// The receiver drop records what it releases and fails on every call
		// when a failure was configured — the same injectable the table
		// above uses, and the only one expressible, because a drop cannot
		// see which pipeline position the value it is releasing came from.
		// The torn draft inverted this: it failed when dropFailAt was -1,
		// which is the NOTHING-FAILS case, and never injected a failure
		// otherwise — so the drop half of every fuzz input was fiction
		// and the invariants below held anyway. That is exactly the
		// vacuous pass this file exists to make impossible.
		var drop traits.Drop[*node] = func(v *node) error {
			seen = append(seen, v)

			if dropFailAt >= 0 {
				return errDropFails
			}

			return nil
		}

		clone, err := drop.Clone(stepClones(steps, failAt)...)
		if err != nil {
			t.Fatal(err)
		}

		got, _ := clone(&node{tag: inputTag, acc: 7})

		counts := map[*node]int{}

		for _, v := range seen {
			counts[v]++

			if v.tag == inputTag {
				t.Fatalf("the caller's own value was released (steps %v, failAt %d, dropFailAt %d)", steps, failAt, dropFailAt)
			}

			if v.tag >= len(steps) {
				t.Fatalf("released a value no step produced: tag %d", v.tag)
			}
		}

		for v, n := range counts {
			if n > 1 {
				t.Fatalf("tag %d was released %d times", v.tag, n)
			}
		}

		if got != nil {
			if counts[got] > 0 {
				t.Fatalf("the value returned was also released (tag %d)", got.tag)
			}
		}
	})
}

// Neither composer recovers. The Drop and Clone types say a callback must
// return normally, and the reason is here: a panic in the middle of a
// composition leaves the steps after it unrun, so the resources they would
// have released are not released. Recovering would turn that into an error
// about one callback at the cost of hiding that the rest never ran.
func TestCompositionDoesNotRecoverFromAPanickingStep(t *testing.T) {
	t.Run("a panicking drop stops the ones after it", func(t *testing.T) {
		var released []int

		drop, err := traits.ComposeDrops(
			func(int) error { released = append(released, 1); return nil },
			func(int) error { panic("boom") },
			func(int) error { released = append(released, 3); return nil },
		)
		if err != nil {
			t.Fatal(err)
		}

		recovered := func() (value any) {
			defer func() { value = recover() }()

			_ = drop(1)

			return nil
		}()

		if recovered == nil {
			t.Fatal("the panic did not reach the caller")
		}

		// The step before it ran, and the one after it did not. That is the
		// whole reason the type says a Drop must not panic.
		if !reflect.DeepEqual(released, []int{1}) {
			t.Fatalf("released = %v, want [1]: the step after the panic is unrun", released)
		}
	})

	t.Run("a panicking clone propagates", func(t *testing.T) {
		clone, err := traits.ComposeClones(
			func(v int) (int, error) { panic("boom") },
			func(v int) (int, error) { return v, nil },
		)
		if err != nil {
			t.Fatal(err)
		}

		defer func() {
			if recover() == nil {
				t.Fatal("the panic did not reach the caller")
			}
		}()

		_, _ = clone(1)
	})
}
