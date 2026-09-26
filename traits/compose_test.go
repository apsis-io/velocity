package traits_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/apsis-io/velocity/traits"
)

// traits_test.go covers the paths these three were written for. The tests here
// cover the claims their doc comments make that nothing was checking, because
// every one of them is a place a mutation walks straight through.
//
// The shared theme: a composer has three inputs and two of them are the values
// that matter. Nothing tested that the caller's value reaches the FIRST clone,
// that every drop is handed the caller's value rather than a derived one, that
// a composition where nothing fails reports no error at all, or what a
// single-step composition does — which is the degenerate case every one of
// these has, and the one a caller writes first.

func TestComposeDropsPassesTheCallersValueToEveryDrop(t *testing.T) {
	var seen []int

	drop, err := traits.ComposeDrops(
		func(value int) error { seen = append(seen, value); return nil },
		func(value int) error { seen = append(seen, value); return nil },
		func(value int) error { seen = append(seen, value); return nil },
	)
	if err != nil {
		t.Fatal(err)
	}

	if err := drop(42); err != nil {
		t.Fatal(err)
	}

	// Every drop releases something held BY the value, so handing one of them
	// a different value would release the wrong resource. Three identical
	// entries, not "three entries": a composer that derived or advanced the
	// value between drops would pass a length check.
	if !reflect.DeepEqual(seen, []int{42, 42, 42}) {
		t.Fatalf("the drops saw %v, want the caller's own value three times", seen)
	}
}

// A composition where nothing failed reports no error. The existing test has
// two of three drops failing, so it cannot tell an empty join from a joined
// nil — and an empty join is what `errors.Join` returns for no arguments, so
// the whole no-failure path rested on that being true rather than on anything
// being asserted.
func TestComposeDropsReportsNoErrorWhenEveryDropSucceeds(t *testing.T) {
	drop, err := traits.ComposeDrops(
		func(int) error { return nil },
		func(int) error { return nil },
	)
	if err != nil {
		t.Fatal(err)
	}

	if err := drop(1); err != nil {
		t.Fatalf("drop = %v, want nil when nothing failed", err)
	}
}

// The caller's value reaches the first clone. A composer that seeded the
// pipeline with a zero T would return a plausible wrong answer for any T whose
// first step is additive, which is most of them.
func TestComposeClonesSeedsTheFirstCloneWithTheCallersValue(t *testing.T) {
	var seen []int

	clone, err := traits.ComposeClones(
		func(value int) (int, error) { seen = append(seen, value); return value + 1, nil },
		func(value int) (int, error) { seen = append(seen, value); return value * 10, nil },
	)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := clone(7); err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(seen, []int{7, 8}) {
		t.Fatalf("the clones saw %v, want the caller's value then the first result", seen)
	}
}

// A single-step composition is the first thing anybody writes, and for
// Drop.Clone it is the interesting one: the only intermediate IS the final
// result, and the final result is never dropped. So a one-step Drop.Clone must
// release nothing at all, and one that releases it would drop the value it was
// asked to return.
func TestSingleStepCompositions(t *testing.T) {
	t.Run("one drop", func(t *testing.T) {
		called := 0

		drop, err := traits.ComposeDrops(func(int) error { called++; return nil })
		if err != nil {
			t.Fatal(err)
		}

		if err := drop(1); err != nil {
			t.Fatal(err)
		}

		if called != 1 {
			t.Fatalf("the single drop ran %d times", called)
		}
	})

	t.Run("one clone", func(t *testing.T) {
		clone, err := traits.ComposeClones(func(value int) (int, error) { return value + 5, nil })
		if err != nil {
			t.Fatal(err)
		}

		if got, err := clone(1); got != 6 || err != nil {
			t.Fatalf("clone = (%d, %v), want (6, nil)", got, err)
		}
	})

	t.Run("one clone with a drop", func(t *testing.T) {
		dropped := 0

		drop := traits.Drop[int](func(int) error { dropped++; return nil })

		clone, err := drop.Clone(func(value int) (int, error) { return value + 5, nil })
		if err != nil {
			t.Fatal(err)
		}

		got, err := clone(1)
		if got != 6 || err != nil {
			t.Fatalf("clone = (%d, %v), want (6, nil)", got, err)
		}

		// The value it produced is the value it returns. Releasing it here
		// would hand back a released resource.
		if dropped != 0 {
			t.Fatalf("a one-step composition released %d values, want none: the only intermediate is the result", dropped)
		}
	})
}

// The distinction both doc comments rest on, made observable. The same clones
// through the two composers produce the same value and the same error; the
// only difference is what got released. Everything else about them is
// identical, which is exactly why the release is the whole argument for having
// two functions.
func TestComposeClonesAndDropCloneDifferOnlyInWhatTheyRelease(t *testing.T) {
	boom := errors.New("boom")

	// Named as Clone rather than as a bare func so both composers accept the
	// same slice: []func(int) (int, error) does not convert to []Clone[int],
	// and re-declaring the steps for each composer would let them drift.
	steps := []traits.Clone[int]{
		func(value int) (int, error) { return value + 1, nil },
		func(int) (int, error) { return 0, boom },
		func(value int) (int, error) { return value * 2, nil },
	}

	plain, err := traits.ComposeClones(steps...)
	if err != nil {
		t.Fatal(err)
	}

	var released []int

	drop := traits.Drop[int](func(value int) error { released = append(released, value); return nil })

	owned, err := drop.Clone(steps...)
	if err != nil {
		t.Fatal(err)
	}

	plainValue, plainErr := plain(10)
	ownedValue, ownedErr := owned(10)

	// Same answer, same failure. The two are interchangeable for the value.
	if plainValue != ownedValue || !errors.Is(plainErr, boom) || !errors.Is(ownedErr, boom) {
		t.Fatalf("plain = (%d, %v), owned = (%d, %v), want the same value and the same error", plainValue, plainErr, ownedValue, ownedErr)
	}

	// And the one thing that is not the same: the intermediate the failing
	// step superseded, which one composer releases and the other cannot.
	if !reflect.DeepEqual(released, []int{11}) {
		t.Fatalf("released = %v, want [11]: the intermediate superseded by the failing step", released)
	}
}
