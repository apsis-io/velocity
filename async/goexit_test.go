package async_test

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/apsis-io/velocity/async"
	"github.com/apsis-io/velocity/traits"
)

// A callback that ends through runtime.Goexit unwinds its goroutine without
// returning, and every way of reporting an outcome by writing it *after* the
// call then silently skips it. All three of the collection operations were
// written that way, and all three were wrong in a different direction:
//
//	Gather  the slot kept the zero Outcome — Err nil, Value zero, Index zero —
//	        so a task that never came back was indistinguishable from a
//	        successful first task.
//	Map     the item's slot kept a zero value with no error, and a worker that
//	        stopped took every item it would have claimed next with it. Those
//	        stranded items were filed as failures carrying context.Cause, which
//	        is nil for a context nobody cancelled: a non-nil error wrapping
//	        nothing, where errors.Is, errors.As and the log message all say
//	        nothing while `if err != nil` insists something failed.
//	Race    the completion was never sent, so the collector waited for
//	        something no amount of waiting produces — until the caller's context
//	        ended and Race reported the CONTEXT's cause, which is a different
//	        failure from the one that happened.
//
// It is reachable from ordinary test code, because t.Fatal and t.FailNow call
// runtime.Goexit: a task that asserts and stops takes its whole worker with it.
//
// The fix in all three is the same shape, and the point of these tests is that
// the *direction* of each wrong answer is asserted, not merely that something
// is non-nil — a caller matching on ErrCallbackExit has to be able to tell
// this apart from a task that returned an error of its own.

func TestGatherReportsATaskThatExitedWithoutReturning(t *testing.T) {
	run := runner(t, async.Limited(4))

	outcomes, err := run.Gather(context.Background(),
		async.Named("ran", func(context.Context) (int, error) { return 1, nil }),
		async.Named("exited", func(context.Context) (int, error) {
			runtime.Goexit()

			return 0, nil
		}),
	)

	if !errors.Is(err, traits.ErrCallbackExit) {
		t.Fatalf("Gather = %v, want ErrCallbackExit", err)
	}

	// The slot must identify WHICH task, or a caller cannot tell the two apart:
	// the zero Outcome has Index 0, so an unwritten slot is a second "task 0".
	if len(outcomes) != 2 {
		t.Fatalf("Gather returned %d outcomes, want 2", len(outcomes))
	}

	if outcomes[1].Index != 1 || outcomes[1].Label != "exited" {
		t.Fatalf("the second outcome is %+v, want the exited task at index 1", outcomes[1])
	}

	if !errors.Is(outcomes[1].Err, traits.ErrCallbackExit) {
		t.Fatalf("the exited task reported %v, want ErrCallbackExit", outcomes[1].Err)
	}

	// And the task that did return is untouched, with its own index intact.
	if outcomes[0].Index != 0 || outcomes[0].Value != 1 || outcomes[0].Err != nil {
		t.Fatalf("the first outcome is %+v, want a plain success at index 0", outcomes[0])
	}
}

// The stranded case is the one Map cannot see: a worker that stops takes the
// items it would have claimed next with it, and nothing about the collection
// says why they are missing.
func TestMapReportsItemsStrandedByAWorkerThatExited(t *testing.T) {
	// One worker, so the exit takes the only worker there is.
	run := runner(t, async.Limited(1))

	results, err := run.Map(context.Background(), []int{1, 2, 3, 4},
		func(_ context.Context, v int) (int, error) {
			if v == 2 {
				runtime.Goexit()
			}

			return v * 10, nil
		})

	// Every missing item must carry an error that says something. The old
	// answer was an ItemError whose Err was nil, which passed `err != nil` and
	// told a reader nothing at all.
	failures := async.Failures(err)
	if len(failures) != 3 {
		t.Fatalf("Map reported %d failures, want 3: %v", len(failures), err)
	}

	// The dying item and the stranded ones are NOT the same event, and saying
	// so is the point: the first ran a callback that exited, the others never
	// ran at all. ErrCallbackExit on an item no worker ever claimed would be a
	// claim about a callback that was never entered.
	if !errors.Is(failures[0].Err, traits.ErrCallbackExit) {
		t.Fatalf("the item that exited carries %v, want ErrCallbackExit", failures[0].Err)
	}

	for _, failure := range failures[1:] {
		if !errors.Is(failure.Err, async.ErrWorkerExit) {
			t.Fatalf("stranded item %d carries %v, want ErrWorkerExit", failure.Index, failure.Err)
		}
	}

	// And the distinction survives to a caller, which is the only place it
	// matters: the join alone cannot tell one worker dying from one callback
	// misbehaving, and errors.Is is the only channel that gets there.
	if errors.Is(failures[0].Err, async.ErrWorkerExit) {
		t.Fatal("an item that ran is also reporting as unclaimed")
	}

	if err == nil {
		t.Fatal("Map reported success for a collection it did not finish")
	}

	// A failed item's slot is the zero value, which is the package's rule and
	// the reason the results below cannot be mistaken for real output.
	if results[0] != 10 {
		t.Fatalf("the item that ran = %d, want 10", results[0])
	}

	for _, i := range []int{1, 2, 3} {
		if results[i] != 0 {
			t.Fatalf("failed item %d = %d, want the zero value", i, results[i])
		}
	}
}

// Race is the one that waited rather than misreported, so the assertion is
// about time as much as about the error: a fix that merely made it eventually
// return would still be a fix worth having, but the test has to notice if it
// goes back to waiting for a completion nobody sends.
func TestRaceReportsATaskThatExitedWithoutReturning(t *testing.T) {
	run := runner(t, async.Limited(4))

	type result struct {
		outcome async.Outcome[int]
		err     error
	}

	results := make(chan result, 1)

	go func() {
		outcome, err := run.Race(context.Background(),
			async.Named("exited", func(context.Context) (int, error) {
				runtime.Goexit()

				return 0, nil
			}))

		results <- result{outcome, err}
	}()

	select {
	case got := <-results:
		if !errors.Is(got.err, traits.ErrCallbackExit) {
			t.Fatalf("Race = (%+v, %v), want ErrCallbackExit", got.outcome, got.err)
		}

		if got.outcome.Label != "exited" {
			t.Fatalf("the outcome is %+v, want the exited task", got.outcome)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Race is still waiting for a completion that will never be sent")
	}
}

// The hooks cover tasks, so a task that never returned is a task that never
// reported. Before the fix these fired for the tasks around it and skipped the
// one that mattered, which is the worst shape for a histogram: a caller sees
// fewer tasks than they submitted and cannot tell which is missing.
//
// The error matters as much as the count, and the first version of this test
// did not check it — a hook that fired with a nil error for the exited task
// passed, which is a real defect shape: an OnTaskComplete bucketed by outcome
// would file a task failure under success.
func TestHooksReportATaskThatExitedWithoutReturning(t *testing.T) {
	type report struct {
		index int
		label string
		err   error
	}

	var (
		mu       sync.Mutex
		reported []report
	)

	hooks := async.Hooks{OnTaskComplete: func(index int, label string, _, _ time.Duration, err error) {
		mu.Lock()

		reported = append(reported, report{index, label, err})
		mu.Unlock()
	}}

	run := runner(t, async.Limited(2), async.WithHooks(hooks))

	if _, err := run.Gather(context.Background(),
		async.Named("ran", func(context.Context) (int, error) { return 1, nil }),
		async.Named("exited", func(context.Context) (int, error) {
			runtime.Goexit()

			return 0, nil
		}),
	); !errors.Is(err, traits.ErrCallbackExit) {
		t.Fatalf("Gather = %v, want ErrCallbackExit", err)
	}

	mu.Lock()
	defer mu.Unlock()

	if len(reported) != 2 {
		t.Fatalf("the hook heard about %v, want both tasks including the one that exited", reported)
	}

	// Both, and each carrying what actually happened to it. Indexed by the
	// reported index rather than by position: the hook runs in each task's own
	// goroutine, so the reports arrive in completion order, and the exited
	// task is the one most likely to arrive first.
	byIndex := map[int]report{}
	for _, r := range reported {
		byIndex[r.index] = r
	}

	if got := byIndex[0]; got.label != "ran" || got.err != nil {
		t.Fatalf("the report for task 0 is %+v, want the task that ran, with no error", got)
	}

	// The assertion that was missing: the count alone passed against a hook
	// that reported a nil error here, which would file a task failure as a
	// success in any histogram bucketed by outcome.
	if got := byIndex[1]; got.label != "exited" || !errors.Is(got.err, traits.ErrCallbackExit) {
		t.Fatalf("the report for task 1 is %+v, want the exited task carrying ErrCallbackExit", got)
	}
}
