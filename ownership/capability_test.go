package ownership_test

import (
	"errors"
	"testing"

	"github.com/apsis-io/velocity/ownership"
)

// A function that asks for Viewer cannot mutate, whichever handle it is
// handed; the compile-time assertions in capability.go pin which handles
// satisfy which capability.
func describe(v ownership.Viewer[[]int]) (int, error) {
	return ownership.View(v, func(values []int) (int, error) { return len(values), nil })
}

func grow(m ownership.Mutator[[]int]) error {
	return m.WithWrite(func(values *[]int) error { *values = append(*values, 0); return nil })
}

func TestCapabilitiesAcrossHandles(t *testing.T) {
	owner := mustOwner(t, []int{1, 2})
	if err := grow(owner); err != nil {
		t.Fatal(err)
	}
	if n, err := describe(owner); err != nil || n != 3 {
		t.Fatalf("describe(owner) = (%d, %v)", n, err)
	}

	shared, err := owner.IntoShared()
	if err != nil {
		t.Fatal(err)
	}
	if err := grow(shared); err != nil {
		t.Fatal(err)
	}
	if n, err := describe(shared); err != nil || n != 4 {
		t.Fatalf("describe(shared) = (%d, %v)", n, err)
	}

	thawed, err := shared.IntoOwner()
	if err != nil {
		t.Fatal(err)
	}
	frozen, err := thawed.Freeze()
	if err != nil {
		t.Fatal(err)
	}
	defer frozen.Release()
	if n, err := describe(frozen); err != nil || n != 4 {
		t.Fatalf("describe(frozen) = (%d, %v)", n, err)
	}
}

func TestPackageLevelViewAndMutateMirrorTheMethods(t *testing.T) {
	owner := mustOwner(t, 10)
	defer owner.Release()

	committed := errors.New("committed")
	got, err := ownership.Mutate(owner, func(value *int) (int, error) {
		*value += 5
		return *value, committed
	})
	if got != 15 || !errors.Is(err, committed) {
		t.Fatalf("Mutate = (%d, %v), want (15, committed)", got, err)
	}
	if value, err := ownership.View(owner, func(value int) (int, error) { return value, nil }); err != nil || value != 15 {
		t.Fatalf("View = (%d, %v)", value, err)
	}

	if _, err := ownership.View[int, int](nil, func(int) (int, error) { return 0, nil }); !errors.Is(err, ownership.ErrReleased) {
		t.Fatalf("nil Viewer = %v", err)
	}
	if _, err := ownership.View[int, int](owner, nil); !errors.Is(err, ownership.ErrProjection) {
		t.Fatalf("nil fn = %v", err)
	}
	var released *ownership.Owner[int]
	if _, err := ownership.Mutate(released, func(*int) (int, error) { return 0, nil }); !errors.Is(err, ownership.ErrReleased) {
		t.Fatalf("nil owner = %v", err)
	}
}
