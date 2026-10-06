package async_test

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/apsis-io/velocity/async"
	"github.com/apsis-io/velocity/traits"
)

// Submit is the Future-shaped submission: start now, ask later. The tests below
// are the shape rather than the plumbing, and three of them are about what a
// caller can conclude from the handle rather than about what it carries.

func TestSubmitResolvesWithTheFunctionsOutcome(t *testing.T) {
	run, err := async.New(async.Limited(2))
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()

	// The work has not run yet: Submit returns before the function is entered.
	entered := make(chan struct{})
	release := make(chan struct{})

	f := run.Submit(ctx, func(context.Context) (int, error) {
		close(entered)
		<-release

		return 42, nil
	})

	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("the submitted function never ran")
	}

	// Unresolved while it runs, and Try says so without blocking.
	if _, ready := f.Try(); ready {
		t.Fatal("Try reported a result for work that is still running")
	}

	close(release)

	res, err := f.Await(ctx)
	if err != nil || res.Value != 42 || !res.Ok() {
		t.Fatalf("Await = (%+v, %v), want 42 and no error", res, err)
	}
}

func TestSubmitReportsTheFunctionsFailure(t *testing.T) {
	run := runner(t, async.Limited(2))
	boom := errors.New("boom")

	f := run.Submit(context.Background(), func(context.Context) (string, error) {
		return "", boom
	})

	res, err := f.Await(context.Background())
	// The error is the work's failure, reachable through both channels: the
	// Result carries it and Await's wraps it, so `if err != nil` catches a
	// failure without the caller looking inside the Result first.
	if !errors.Is(err, boom) || !errors.Is(res.Err, boom) {
		t.Fatalf("Await = (%+v, %v), want the function's own error in both", res, err)
	}

	if res.Value != "" {
		t.Fatalf("Await = %+v, want the zero value beside a failure", res)
	}
}

// A Future is droppable: nothing is abandoned, so nothing needs cleaning up by
// whoever let go. The work still runs and still resolves.
func TestSubmitCompletesAHandleNobodyAwaits(t *testing.T) {
	run := runner(t, async.Limited(2))

	completed := make(chan struct{})

	f := run.Submit(context.Background(), func(context.Context) (int, error) {
		defer close(completed)

		return 7, nil
	})

	_ = f // dropped on the floor, exactly as a caller who changed its mind would

	select {
	case <-completed:
	case <-time.After(2 * time.Second):
		t.Fatal("dropping the handle stopped the work")
	}

	// And it did not stop the resolution either, which is the half that makes
	// dropping safe rather than merely quiet.
	deadline := time.Now().Add(2 * time.Second)

	for {
		res, ready := f.Try()
		if ready {
			if res.Value != 7 {
				t.Fatalf("the dropped handle resolved to %+v, want 7", res)
			}

			return
		}

		if time.Now().After(deadline) {
			t.Fatal("a dropped handle never resolved")
		}

		time.Sleep(time.Millisecond)
	}
}

// **A Future always resolves.** These two are the reason Submit recovers what
// Gather lets escape: a handle that never resolves reports every awaiter's
// timeout as the work's failure, which is the one thing Await promises not to
// do. A panic that reached the process instead would be worse, and a Submit
// task panics somewhere its submitter has already left.
func TestSubmitReportsAPanicAsAFailedTask(t *testing.T) {
	run := runner(t, async.Limited(2))

	f := run.Submit(context.Background(), func(context.Context) (int, error) {
		panic("boom")
	})

	res, err := f.Await(context.Background())

	var panicked *traits.Panic
	if !errors.As(err, &panicked) {
		t.Fatalf("Await = %v, want a *traits.Panic", err)
	}

	if panicked.Value != "boom" || len(panicked.Stack) == 0 {
		t.Fatalf("the panic = %+v, want the value and a stack", panicked)
	}

	// The failure is in the Result too, not only in the wait's error.
	if !errors.Is(res.Err, error(panicked)) {
		t.Fatalf("Result.Err = %v, want the panic", res.Err)
	}
}

// runtime.Goexit is the other way a callback can end without returning. The
// deferred function above still runs, so the handle resolves — with the fact
// that nothing came back, rather than with the success the zero value would
// otherwise report.
func TestSubmitReportsAExitWithoutReturning(t *testing.T) {
	run := runner(t, async.Limited(2))

	f := run.Submit(context.Background(), func(context.Context) (int, error) {
		runtime.Goexit()

		return 0, nil
	})

	res, err := f.Await(context.Background())
	if !errors.Is(err, traits.ErrCallbackExit) {
		t.Fatalf("Await = %v, want ErrCallbackExit", err)
	}

	if res.Value != 0 || res.Ok() {
		t.Fatalf("Await = %+v, want a zero value reported as not-succeeded", res)
	}
}

// The validating cases are refused without a goroutine and without a hook: a
// function that was never a function is not a task, so there is nothing to
// report. The handle still comes back, already resolved, because a Future a
// caller cannot hold is not a Future.
func TestSubmitRefusesMalformedSubmissions(t *testing.T) {
	for _, tt := range []struct {
		name string
		run  *async.Runner
		ctx  context.Context
		fn   func(context.Context) (int, error)
		want error
	}{
		{
			name: "a nil Runner",
			ctx:  context.Background(),
			fn:   func(context.Context) (int, error) { return 0, nil },
			want: async.ErrNilReceiver,
		},
		{
			name: "a nil function",
			run:  runner(t, async.Limited(2)),
			ctx:  context.Background(),
			want: async.ErrNilTask,
		},
		{
			name: "a nil context",
			run:  runner(t, async.Limited(2)),
			fn:   func(context.Context) (int, error) { return 0, nil },
			want: async.ErrNilContext,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := tt.run.Submit(tt.ctx, tt.fn)

			res, err := f.Await(context.Background())
			if !errors.Is(err, tt.want) {
				t.Fatalf("Await = %v, want %v", err, tt.want)
			}

			if res.Ok() {
				t.Fatalf("Await = %+v, want a refusal reported as not-succeeded", res)
			}
		})
	}
}

// A submission's context is the work's, not the wait's: cancelling it is how a
// caller stops work it has already started, and the handle reports the cause
// while the function — if it is cooperative — stops.
func TestSubmitPassesTheCallersContextToTheFunction(t *testing.T) {
	run := runner(t, async.Limited(2))

	ctx, cancel := context.WithCancel(context.Background())

	var seen error

	f := run.Submit(ctx, func(ctx context.Context) (int, error) {
		<-ctx.Done()
		seen = context.Cause(ctx)

		return 0, ctx.Err()
	})

	cancel()

	if _, err := f.Await(context.Background()); err == nil {
		t.Fatal("a cancelled submission reported success")
	}

	if !errors.Is(seen, context.Canceled) {
		t.Fatalf("the function saw %v, want the context's own cause", seen)
	}
}

// Await bounds the wait and not the work: giving up says something about the
// caller's patience, and the handle is still resolved for anyone watching.
func TestAwaitTimeoutLeavesTheWorkRunning(t *testing.T) {
	run := runner(t, async.Limited(2))

	var (
		release  = make(chan struct{})
		finished = make(chan struct{})
	)

	f := run.Submit(context.Background(), func(context.Context) (int, error) {
		<-release
		close(finished)

		return 5, nil
	})

	short, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	res, err := f.Await(short)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Await = %v, want the deadline", err)
	}

	// The wait's failure is not the work's: the zero Result reports Ok, and the
	// doc says so, because the Result is not to be read when the wait did not
	// complete.
	if !res.Ok() {
		t.Fatalf("Await = %+v, want the zero Result, which is a succeeded zero", res)
	}

	// And the work is untouched — asserted by the work finishing at all, not by
	// a later poll, so a regression fails here instead of hanging.
	close(release)

	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("giving up on the wait stopped the work")
	}

	// The result is published after the work's function returns, which is
	// after finished closes, so readiness is awaited under a bound rather
	// than read on the spot: a Try here raced the publication and read
	// ready=false off a healthy runner.
	bounded, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	got, err := f.Await(bounded)
	if err != nil {
		t.Fatalf("the work never became readable after finishing: %v", err)
	}
	if got.Value != 5 || got.Err != nil {
		t.Fatalf("the work = %+v, want 5 with no error once it finished", got)
	}
}

// The limit is per operation, so a one-task operation is not bounded by it —
// which is a property of the Runner and is documented as such. What matters
// here is that a Limit of one does not serialise two Submits against each
// other, because a caller reading "a Limit bounds goroutines" everywhere else
// in the package would expect it to.
func TestSubmitIsNotBoundedByTheLimit(t *testing.T) {
	run := runner(t, async.Limited(1))

	var (
		entered     atomic.Int32
		both        = make(chan struct{})
		bothEntered = make(chan struct{})
	)

	blocking := func(id int) func(context.Context) (int, error) {
		return func(context.Context) (int, error) {
			if entered.Add(1) == 2 {
				close(bothEntered)
			}

			<-both

			return id, nil
		}
	}

	first := run.Submit(context.Background(), blocking(1))
	second := run.Submit(context.Background(), blocking(2))

	// Both inside their functions at once, which a limit of one would prevent.
	// Releasing on the failure path keeps a regression a failure rather than a
	// suite that hangs until the test binary's own timeout.
	select {
	case <-bothEntered:
	case <-time.After(2 * time.Second):
		close(both)
		t.Fatal("a Limit of 1 serialised two Submits; a limit is per operation")
	}

	close(both)

	for i, f := range []*traits.Future[int]{first, second} {
		if _, err := f.Await(context.Background()); err != nil {
			t.Fatalf("Await %d = %v", i, err)
		}
	}
}

// The hook fires once per Submit, including for a task that ended badly, and
// before the handle resolves so that awaiting a Future and then reading what
// the hook recorded is not a race.
func TestSubmitReportsEachTaskToTheHooks(t *testing.T) {
	for _, tt := range []struct {
		name string
		fn   func(context.Context) (int, error)
	}{
		{
			name: "a task that ran",
			fn:   func(context.Context) (int, error) { return 1, nil },
		},
		{
			name: "a task that failed",
			fn:   func(context.Context) (int, error) { return 0, errors.New("boom") },
		},
		{
			name: "a task that panicked",
			fn:   func(context.Context) (int, error) { panic("boom") },
		},
		{
			name: "a task that exited without returning",
			fn:   func(context.Context) (int, error) { runtime.Goexit(); return 0, nil },
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var (
				mu      sync.Mutex
				reports int
				seen    []int
			)

			hooks := async.Hooks{OnTaskComplete: func(index int, label string, _, _ time.Duration, _ error) {
				mu.Lock()
				reports++

				seen = append(seen, index)
				mu.Unlock()

				// A submitted task has no collection to be an index of and no
				// name, and -1 is how this package says both.
				if index != -1 || label != "" {
					t.Errorf("the hook saw (%d, %q), want (-1, \"\")", index, label)
				}
			}}

			run := runner(t, async.Limited(2), async.WithHooks(hooks))

			// Awaiting is what makes the count deterministic: the hook fires
			// before the handle resolves, so a resolved Future means the task has
			// been reported. Counting after a sleep would be testing the race.
			_, _ = run.Submit(context.Background(), tt.fn).Await(context.Background())

			mu.Lock()
			defer mu.Unlock()

			if reports != 1 {
				t.Fatalf("the hook fired %d times, want 1", reports)
			}

			if len(seen) != 1 {
				t.Fatalf("the hook saw %v, want one report", seen)
			}
		})
	}
}

// A submission refused before it started is still a task the contract covers —
// except a malformed one, which never was a task at all.
func TestSubmitReportsNothingForAMalformedSubmission(t *testing.T) {
	var (
		mu      sync.Mutex
		reports int
	)

	hooks := async.Hooks{OnTaskComplete: func(_ int, _ string, _, _ time.Duration, _ error) {
		mu.Lock()
		reports++
		mu.Unlock()
	}}

	run := runner(t, async.Limited(2), async.WithHooks(hooks))

	// The type argument is explicit because a nil function gives the compiler
	// nothing to infer T from, which is the malformed case caught at compile
	// time rather than at the call.
	if _, err := run.Submit[int](context.Background(), nil).Await(context.Background()); !errors.Is(err, async.ErrNilTask) {
		t.Fatalf("Await = %v, want ErrNilTask", err)
	}

	mu.Lock()
	defer mu.Unlock()

	if reports != 0 {
		t.Fatalf("the hook fired %d times for a submission that was never a task", reports)
	}
}
