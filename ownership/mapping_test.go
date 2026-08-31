package ownership_test

import (
	"errors"
	"strconv"
	"sync"
	"testing"

	"github.com/apsis-io/velocity/ownership"
)

func TestMapTransfersAndChainsDropInOrder(t *testing.T) {
	var mu sync.Mutex
	var order []string
	record := func(name string) {
		mu.Lock()
		order = append(order, name)
		mu.Unlock()
	}

	source := mustOwner(t, 21, ownership.WithDrop(func(int) error {
		record("source")
		return nil
	}))

	derived, err := source.Map(
		func(value int) (string, error) { return strconv.Itoa(value * 2), nil },
		ownership.WithDrop(func(string) error {
			record("derived")
			return nil
		}),
	)
	if err != nil {
		t.Fatal(err)
	}

	if state := source.State(); !state.Moved {
		t.Fatalf("source after Map = %+v", state)
	}
	if _, err := source.Borrow(); !errors.Is(err, ownership.ErrMoved) {
		t.Fatalf("source Borrow after Map = %v", err)
	}

	got, err := derived.Read(func(access ownership.ReadAccess[string]) (string, error) {
		return access.Project(func(value string) (string, error) { return value, nil })
	})
	if err != nil || got != "42" {
		t.Fatalf("derived value = (%q, %v)", got, err)
	}

	mu.Lock()
	if len(order) != 0 {
		t.Fatalf("drops ran before release: %v", order)
	}
	mu.Unlock()

	if err := derived.Release(); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	// Derived first, then source: unwrap before closing what it wrapped.
	if len(order) != 2 || order[0] != "derived" || order[1] != "source" {
		t.Fatalf("drop order = %v, want [derived source]", order)
	}
}

func TestMapPreservesSourceDropWithoutDerivedDrop(t *testing.T) {
	dropped := make(chan int, 1)
	source := mustOwner(t, 7, ownership.WithDrop(func(value int) error {
		dropped <- value
		return nil
	}))

	derived, err := source.Map(func(value int) (string, error) { return strconv.Itoa(value), nil })
	if err != nil {
		t.Fatal(err)
	}
	if err := derived.Release(); err != nil {
		t.Fatal(err)
	}
	select {
	case value := <-dropped:
		if value != 7 {
			t.Fatalf("source drop got %d", value)
		}
	default:
		t.Fatal("source drop did not run through the derived owner")
	}
}

func TestMapJoinsBothDropErrors(t *testing.T) {
	sourceErr := errors.New("source")
	derivedErr := errors.New("derived")
	source := mustOwner(t, 1, ownership.WithDrop(func(int) error { return sourceErr }))

	derived, err := source.Map(
		func(value int) (int, error) { return value, nil },
		ownership.WithDrop(func(int) error { return derivedErr }),
	)
	if err != nil {
		t.Fatal(err)
	}
	err = derived.Release()
	if !errors.Is(err, sourceErr) || !errors.Is(err, derivedErr) {
		t.Fatalf("Release = %v, want both drop errors", err)
	}
}

func TestMapLeavesSourceUsableWhenFnFails(t *testing.T) {
	wantErr := errors.New("nope")
	source := mustOwner(t, 5)
	defer source.Release()

	if _, err := source.Map(func(int) (string, error) { return "", wantErr }); !errors.Is(err, wantErr) {
		t.Fatalf("Map = %v", err)
	}

	// The failed Map must not have consumed the owner or leaked its lease.
	if state := source.State(); state.Moved || state.Released || state.Writer || state.Readers != 0 {
		t.Fatalf("source after failed Map = %+v", state)
	}
	value, err := source.Read(func(access ownership.ReadAccess[int]) (int, error) {
		return access.Project(func(value int) (int, error) { return value, nil })
	})
	if err != nil || value != 5 {
		t.Fatalf("source still usable = (%d, %v)", value, err)
	}
}

func TestMapRequiresExclusivity(t *testing.T) {
	source := mustOwner(t, 1)
	defer source.Release()
	borrow, err := source.Borrow()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.Map(func(value int) (int, error) { return value, nil }); !errors.Is(err, ownership.ErrConflict) {
		t.Fatalf("Map with outstanding borrow = %v", err)
	}
	_ = borrow.Release()

	derived, err := source.Map(func(value int) (int, error) { return value + 1, nil })
	if err != nil {
		t.Fatalf("Map after release = %v", err)
	}
	_ = derived.Release()
}

func TestMapValidation(t *testing.T) {
	t.Run("nil fn", func(t *testing.T) {
		owner := mustOwner(t, 1)
		defer owner.Release()
		if _, err := owner.Map[int](nil); !errors.Is(err, ownership.ErrProjection) {
			t.Fatalf("nil fn = %v", err)
		}
	})

	t.Run("nil owner", func(t *testing.T) {
		var owner *ownership.Owner[int]
		if _, err := owner.Map(func(value int) (int, error) { return value, nil }); !errors.Is(err, ownership.ErrReleased) {
			t.Fatalf("nil owner = %v", err)
		}
	})

	t.Run("bad option leaves owner usable", func(t *testing.T) {
		owner := mustOwner(t, 1)
		defer owner.Release()
		_, err := owner.Map(func(value int) (int, error) { return value, nil }, (ownership.Option[int])(nil))
		if !errors.Is(err, ownership.ErrNilOption) {
			t.Fatalf("nil option = %v", err)
		}
		if state := owner.State(); state.Moved || state.Writer {
			t.Fatalf("owner after rejected option = %+v", state)
		}
	})
}

func TestMapDerivedSupportsSnapshot(t *testing.T) {
	source := mustOwner(t, 3)
	derived, err := source.Map(
		func(value int) ([]int, error) { return []int{value}, nil },
		ownership.WithClone(func(value []int) ([]int, error) { return append([]int(nil), value...), nil }),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer derived.Release()

	snapshot, err := derived.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	snapshot[0] = 99
	original, err := derived.Read(func(access ownership.ReadAccess[[]int]) (int, error) {
		return access.Project(func(value []int) (int, error) { return value[0], nil })
	})
	if err != nil || original != 3 {
		t.Fatalf("snapshot leaked: (%d, %v)", original, err)
	}
}
