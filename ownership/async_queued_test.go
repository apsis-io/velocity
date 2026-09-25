package ownership_test

import (
	"context"
	"errors"
	"runtime"
	"testing"
	"time"

	"github.com/apsis-io/velocity/ownership"
)

// A queued mutation whose owner is released or moved must report that, and
// report it promptly. The terminal condition is available the moment the waiter
// re-reads the cell — but a waiter asleep on the broadcast is only woken by a
// borrow ending or a seal, and neither happens on release or move. So the
// mutation sleeps until its context dies and then reports the *context's* cause,
// which is a different failure from the one that actually occurred.
func TestMutateAsyncReportsAReleasedOwnerWhileQueued(t *testing.T) {
	for _, tc := range []struct {
		name  string
		leave func(o *ownership.Owner[int])
		check func(t *testing.T, err error)
	}{
		{
			name:  "released",
			leave: func(o *ownership.Owner[int]) { _ = o.Release() },
			check: func(t *testing.T, err error) {
				var released *ownership.ReleasedError
				if !errors.As(err, &released) {
					t.Fatalf("err = %v, want a ReleasedError", err)
				}
			},
		},
		{
			name:  "moved",
			leave: func(o *ownership.Owner[int]) { _, _ = o.Move() },
			check: func(t *testing.T, err error) {
				var moved *ownership.MovedError
				if !errors.As(err, &moved) {
					t.Fatalf("err = %v, want a MovedError", err)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			owner, err := ownership.New(0)
			if err != nil {
				t.Fatal(err)
			}

			// Hold the borrow so the mutation queues behind it.
			held, err := owner.BorrowMut()
			if err != nil {
				t.Fatal(err)
			}

			// A generous context: the point is that the mutation reports the
			// terminal condition without waiting for it.
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			ran := make(chan struct{})
			f := owner.MutateAsync(ctx, func(v *int) (int, error) {
				close(ran)

				*v = 1

				return *v, nil
			})

			if _, ready := f.Try(); ready {
				t.Fatal("the mutation was admitted while a write borrow was held")
			}

			if err := held.Release(); err != nil {
				t.Fatal(err)
			}

			tc.leave(owner)

			start := time.Now()
			res, err := f.Await(ctx)
			elapsed := time.Since(start)

			if err == nil {
				t.Fatalf("the mutation reported success (%+v) after the owner was %s", res, tc.name)
			}

			if errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("the mutation reported the context cause after %s, not the %s", elapsed, tc.name)
			}

			tc.check(t, err)

			select {
			case <-ran:
				t.Fatal("the callback ran after the owner was gone")
			default:
			}
		})
	}
}

// A callback that calls runtime.Goexit runs the deferred release and then ends
// the goroutine without returning, so `recover` sees nothing and the Future is
// completed with whatever the named return values hold — a zero value and a nil
// error, which reads as success for work that never happened. The package's rule
// is that a callback must return normally, but a panic is reported as a panic
// and a Goexit silently is not.
func TestMutateAsyncDoesNotReportSuccessWhenTheCallbackExits(t *testing.T) {
	owner, err := ownership.New(0)
	if err != nil {
		t.Fatal(err)
	}

	f := owner.MutateAsync(context.Background(), func(*int) (int, error) {
		runtime.Goexit()
		return 99, nil
	})

	// Await must not report the 99 the callback never returned.
	done := make(chan struct{})
	go func() {
		defer close(done)

		_ = f
	}()

	res, err := f.Await(context.Background())
	if err == nil {
		t.Fatalf("a callback that never returned reported success: %+v", res)
	}

	// And the cell is still usable, because the borrow was released on the way
	// out — that part of Goexit behaves.
	if _, err := owner.View(func(v int) (int, error) { return v, nil }); err != nil {
		t.Fatalf("the cell is wedged after a Goexit: %v", err)
	}
}
