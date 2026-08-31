package traits_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/apsis-io/velocity/traits"
)

func TestComposeDropsRunsAllAndJoinsErrors(t *testing.T) {
	errFirst := errors.New("first")
	errThird := errors.New("third")
	var calls []int

	drop, err := traits.ComposeDrops(
		func(int) error { calls = append(calls, 1); return errFirst },
		func(int) error { calls = append(calls, 2); return nil },
		func(int) error { calls = append(calls, 3); return errThird },
	)
	if err != nil {
		t.Fatal(err)
	}

	err = drop(42)
	if !reflect.DeepEqual(calls, []int{1, 2, 3}) {
		t.Fatalf("calls = %v", calls)
	}
	if !errors.Is(err, errFirst) || !errors.Is(err, errThird) {
		t.Fatalf("drop error = %v", err)
	}
}

func TestComposeClonesAppliesSequentially(t *testing.T) {
	clone, err := traits.ComposeClones(
		func(value int) (int, error) { return value + 2, nil },
		func(value int) (int, error) { return value * 3, nil },
	)
	if err != nil {
		t.Fatal(err)
	}

	got, err := clone(4)
	if err != nil {
		t.Fatal(err)
	}
	if got != 18 {
		t.Fatalf("clone(4) = %d, want 18", got)
	}
}

func TestComposeClonesStopsAtFirstError(t *testing.T) {
	wantErr := errors.New("stop")
	called := false
	clone, err := traits.ComposeClones(
		func(value int) (int, error) { return value + 1, nil },
		func(int) (int, error) { return 0, wantErr },
		func(value int) (int, error) { called = true; return value, nil },
	)
	if err != nil {
		t.Fatal(err)
	}

	got, err := clone(1)
	if got != 0 || !errors.Is(err, wantErr) {
		t.Fatalf("clone = (%d, %v)", got, err)
	}
	if called {
		t.Fatal("clone after failure was called")
	}
}

func TestComposeClonesWithDropOwnsOnlyIntermediates(t *testing.T) {
	var dropped []int
	clone, err := traits.ComposeClonesWithDrop(
		func(value int) error { dropped = append(dropped, value); return nil },
		func(value int) (int, error) { return value + 1, nil },
		func(value int) (int, error) { return value + 1, nil },
		func(value int) (int, error) { return value + 1, nil },
	)
	if err != nil {
		t.Fatal(err)
	}

	got, err := clone(10)
	if err != nil {
		t.Fatal(err)
	}
	if got != 13 {
		t.Fatalf("clone = %d, want 13", got)
	}
	if !reflect.DeepEqual(dropped, []int{11, 12}) {
		t.Fatalf("dropped = %v, want [11 12]", dropped)
	}
}

func TestComposeClonesWithDropCleansAfterCloneFailure(t *testing.T) {
	cloneErr := errors.New("clone")
	dropErr := errors.New("drop")
	var dropped []int
	clone, err := traits.ComposeClonesWithDrop(
		func(value int) error { dropped = append(dropped, value); return dropErr },
		func(value int) (int, error) { return value + 1, nil },
		func(int) (int, error) { return 0, cloneErr },
	)
	if err != nil {
		t.Fatal(err)
	}

	got, err := clone(10)
	if got != 0 || !errors.Is(err, cloneErr) || !errors.Is(err, dropErr) {
		t.Fatalf("clone = (%d, %v)", got, err)
	}
	if !reflect.DeepEqual(dropped, []int{11}) {
		t.Fatalf("dropped = %v, want [11]", dropped)
	}
}

func TestComposeClonesWithDropStopsAfterDropFailureAndCleansNext(t *testing.T) {
	dropErr := errors.New("drop")
	var dropped []int
	clone, err := traits.ComposeClonesWithDrop(
		func(value int) error {
			dropped = append(dropped, value)
			if value == 11 {
				return dropErr
			}
			return nil
		},
		func(value int) (int, error) { return value + 1, nil },
		func(value int) (int, error) { return value + 1, nil },
		func(value int) (int, error) { return value + 1, nil },
	)
	if err != nil {
		t.Fatal(err)
	}

	got, err := clone(10)
	if got != 0 || !errors.Is(err, dropErr) {
		t.Fatalf("clone = (%d, %v)", got, err)
	}
	if !reflect.DeepEqual(dropped, []int{11, 12}) {
		t.Fatalf("dropped = %v, want [11 12]", dropped)
	}
}

func TestCompositionValidation(t *testing.T) {
	tests := []struct {
		name string
		run  func() error
		idx  int
	}{
		{"empty drops", func() error { _, err := traits.ComposeDrops[int](); return err }, -1},
		{"nil drop", func() error { _, err := traits.ComposeDrops(append([]traits.Drop[int]{}, nil)...); return err }, 0},
		{"empty clones", func() error { _, err := traits.ComposeClones[int](); return err }, -1},
		{"nil clone", func() error { _, err := traits.ComposeClones(append([]traits.Clone[int]{}, nil)...); return err }, 0},
		{"nil cleanup drop", func() error {
			_, err := traits.ComposeClonesWithDrop[int](nil, func(v int) (int, error) { return v, nil })
			return err
		}, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.run()
			if !errors.Is(err, traits.ErrInvalidComposition) {
				t.Fatalf("error = %v, want ErrInvalidComposition", err)
			}
			var configErr *traits.ConfigError
			if !errors.As(err, &configErr) {
				t.Fatalf("error = %T, want ConfigError", err)
			}
			if configErr.Index != tt.idx {
				t.Fatalf("index = %d, want %d", configErr.Index, tt.idx)
			}
			if tt.idx >= 0 && !errors.Is(err, traits.ErrNilTrait) {
				t.Fatalf("error = %v, want ErrNilTrait", err)
			}
		})
	}
}
