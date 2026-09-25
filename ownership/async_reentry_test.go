package ownership_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/apsis-io/velocity/ownership"
)

// What actually happens when a MutateAsync callback reaches back into its own
// cell, one form at a time. The answer is that every synchronous form already
// errors immediately, and only one shape hangs — which is not a detection
// failure but a caller waiting inside a critical section it holds.
func TestMutateAsyncReentryByForm(t *testing.T) {
	t.Run("View inside the callback errors", func(t *testing.T) {
		owner, err := ownership.New(0)
		if err != nil {
			t.Fatal(err)
		}

		var inner error

		f := owner.MutateAsync(context.Background(), func(v *int) (int, error) {
			_, inner = owner.View(func(got int) (int, error) { return got, nil })
			return *v, nil
		})
		if _, err := f.Await(context.Background()); err != nil {
			t.Fatal(err)
		}

		if !errors.Is(inner, ownership.ErrConflict) {
			t.Fatalf("a re-entrant View = %v, want ErrConflict, not a wait", inner)
		}
	})

	t.Run("Mutate inside the callback errors", func(t *testing.T) {
		owner, err := ownership.New(0)
		if err != nil {
			t.Fatal(err)
		}

		var inner error

		f := owner.MutateAsync(context.Background(), func(v *int) (int, error) {
			_, inner = owner.Mutate(func(got *int) (int, error) { *got = 99; return *got, nil })
			return *v, nil
		})
		if _, err := f.Await(context.Background()); err != nil {
			t.Fatal(err)
		}

		if !errors.Is(inner, ownership.ErrConflict) {
			t.Fatalf("a re-entrant Mutate = %v, want ErrConflict, not a wait", inner)
		}
	})

	t.Run("BorrowMut inside the callback errors", func(t *testing.T) {
		owner, err := ownership.New(0)
		if err != nil {
			t.Fatal(err)
		}

		var inner error

		f := owner.MutateAsync(context.Background(), func(v *int) (int, error) {
			// Released on the success path even though the test expects a
			// refusal: a discarded handle is a leak the analyzer cannot rule
			// out, and it reported exactly that here.
			if borrow, err := owner.BorrowMut(); err != nil {
				inner = err
			} else {
				inner = errors.New("a re-entrant borrow was admitted")
				_ = borrow.Release()
			}

			return *v, nil
		})
		if _, err := f.Await(context.Background()); err != nil {
			t.Fatal(err)
		}

		if !errors.Is(inner, ownership.ErrConflict) {
			t.Fatalf("a re-entrant BorrowMut = %v, want ErrConflict, not a wait", inner)
		}
	})

	// The one shape that waits, and it waits because the caller chose to: the
	// nested mutation queues, and the callback blocks on it until the callback
	// returns. A deadline turns that hang into an error, and the error arrives
	// where the caller is already looking.
	t.Run("awaiting a nested MutateAsync waits, or errors on a deadline", func(t *testing.T) {
		owner, err := ownership.New(0)
		if err != nil {
			t.Fatal(err)
		}

		inner := owner.MutateAsync(context.Background(), func(v *int) (int, error) {
			nested := owner.MutateAsync(context.Background(), func(g *int) (int, error) {
				*g = 1
				return *g, nil
			})

			waitCtx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
			defer cancel()

			if _, err := nested.Await(waitCtx); !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("awaiting a nested mutation = %v, want the deadline", err)
			}

			return *v, nil
		})

		if _, err := inner.Await(context.Background()); err != nil {
			t.Fatal(err)
		}
	})
}
