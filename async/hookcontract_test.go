package async_test

import (
	"context"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/apsis-io/velocity/async"
)

// The Hooks doc is a promise about which submissions are reported. Nothing tied
// that promise to the code: a fourth submission path could be added, or the
// reporting for one of the existing three could change, and the only symptom
// would be a metrics counter quietly disagreeing with the documentation. That
// is the same failure the lostrelease drift test exists to catch, aimed at
// prose instead of a table.
//
// The guard keys on shape rather than names. A submission path is a method
// that takes a func(context.Context) error, so a rename is caught and so is a
// fourth path nobody considered — neither of which a name list would notice.
//
// It has one blind spot, found by being shown the same shape of guard in
// another package: reflect.Methods returns only EXPORTED methods, so an
// unexported submission path — an internal fast path that took the same shape —
// would be invisible here, and would then silently never be reported. It does
// not bite today because all three submission paths are exported, and it is
// recorded rather than left implied. The fix, if it ever matters, is to stop
// reflecting and parse the package with go/ast, which sees unexported methods;
// the price is a test that has to resolve the type by hand, which is why it is
// not done for a case that does not exist yet.

// specifiedSubmissionPaths is every submission path whose hook behaviour the
// table in TestErrGroupHookCountsMatchTheContract spells out. A path missing
// here fails the test below.
var specifiedSubmissionPaths = map[string]bool{
	"Go":        true,
	"GoContext": true,
	"TryGo":     true,
}

// isSubmissionPath reports whether a method hands a function to the group: its
// last parameter is a func(context.Context) error, and it returns nothing or a
// bool. The function is the LAST parameter rather than the only one, because
// GoContext takes a context as well — a shape rule that missed GoContext on its
// first version, which is the kind of thing a contract test should find rather
// than a reason to write the rule loosely.
func isSubmissionPath(m reflect.Method) bool {
	t := m.Type
	if t.NumIn() < 2 { // receiver plus at least the function
		return false
	}

	if t.In(t.NumIn()-1).String() != "func(context.Context) error" {
		return false
	}

	return t.NumOut() <= 1
}

// TestErrGroupSubmissionPathsAreSpecified fails when the API grows a submission
// path the hook contract does not describe, and when the contract names one
// that is gone. Both directions matter: the first is how a new path arrives
// unannounced, the second is how a stale entry hides a rename.
func TestErrGroupSubmissionPathsAreSpecified(t *testing.T) {
	// The pointer, not the value: every method on ErrGroup has a pointer
	// receiver, so the value type's method set is empty and the guard
	// would pass vacuously.
	typ := reflect.TypeFor[*async.ErrGroup]()
	seen := map[string]bool{}

	for m := range typ.Methods() {
		if !isSubmissionPath(m) {
			continue
		}

		seen[m.Name] = true
		if !specifiedSubmissionPaths[m.Name] {
			t.Errorf("ErrGroup.%s submits a function but the hook contract does not specify its reporting.\n"+
				"\tAdd it to specifiedSubmissionPaths and a case to TestErrGroupHookCountsMatchTheContract, "+
				"or say in the Hooks doc why it is not a submission path.", m.Name)
		}
	}

	for name := range specifiedSubmissionPaths {
		if !seen[name] {
			t.Errorf("the hook contract specifies ErrGroup.%s, which no longer takes a function.\n"+
				"\tRemove it, or the contract describes a method that is not there.", name)
		}
	}
}

// TestErrGroupHookCountsMatchTheContract pins the reporting for every outcome,
// including the two that report nothing. Those are the ones worth pinning: a
// caller building a histogram of hook invocations has no way to notice if a
// refusal starts reporting, and the difference between "work that did not
// happen" and "work the caller chose not to submit" is invisible in a count.
func TestErrGroupHookCountsMatchTheContract(t *testing.T) {
	for _, tt := range []struct {
		name string
		want int
		run  func(t *testing.T, eg *async.ErrGroup, released chan struct{})
	}{
		{
			// The function runs, so the hook fires from its own goroutine with
			// a real duration.
			name: "Go, function runs",
			want: 1,
			run: func(t *testing.T, eg *async.ErrGroup, _ chan struct{}) {
				eg.Go(func(context.Context) error { return nil })
			},
		},
		{
			name: "Go, group already finished",
			want: 1,
			run: func(t *testing.T, eg *async.ErrGroup, _ chan struct{}) {
				_ = eg.Wait()
				eg.Go(func(context.Context) error {
					t.Error("a function ran after the group finished")
					return nil
				})
			},
		},
		{
			// A malformed submission is not a submission: there is nothing to
			// report about a function that was never a function.
			name: "Go, nil function",
			want: 0,
			run: func(t *testing.T, eg *async.ErrGroup, _ chan struct{}) {
				eg.Go(nil)
			},
		},
		{
			name: "GoContext, function runs",
			want: 1,
			run: func(t *testing.T, eg *async.ErrGroup, _ chan struct{}) {
				if !eg.GoContext(context.Background(), func(context.Context) error { return nil }) {
					t.Error("GoContext refused a submission with a free permit")
				}
			},
		},
		{
			// Submitted, then discarded by a context that was already done.
			// The caller handed over a function, so it is reported.
			name: "GoContext, caller's context already done",
			want: 1,
			run: func(t *testing.T, eg *async.ErrGroup, _ chan struct{}) {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()

				if eg.GoContext(ctx, func(context.Context) error {
					t.Error("a function ran against a finished context")
					return nil
				}) {
					t.Error("GoContext submitted against a finished context")
				}
			},
		},
		{
			name: "TryGo, function runs",
			want: 1,
			run: func(t *testing.T, eg *async.ErrGroup, _ chan struct{}) {
				if !eg.TryGo(func(context.Context) error { return nil }) {
					t.Error("TryGo refused a submission with a free permit")
				}
			},
		},
		{
			// Refused for want of a permit, reports nothing — the one
			// asymmetry in the contract. The count here is 1 because the
			// function holding the limit reports when it finishes; the refused
			// submission contributes none of it. If TryGo started reporting a
			// refusal, this would read 2.
			name: "TryGo, refused for want of a permit",
			want: 1,
			run: func(t *testing.T, eg *async.ErrGroup, released chan struct{}) {
				started := make(chan struct{})

				eg.Go(func(context.Context) error {
					close(started)
					<-released

					return nil
				})
				<-started

				if eg.TryGo(func(context.Context) error {
					t.Error("TryGo submitted with no permit free")
					return nil
				}) {
					t.Error("TryGo submitted with no permit free")
				}
			},
		},
		{
			name: "TryGo, group already finished",
			want: 1,
			run: func(t *testing.T, eg *async.ErrGroup, _ chan struct{}) {
				_ = eg.Wait()
				if eg.TryGo(func(context.Context) error {
					t.Error("a function ran after the group finished")
					return nil
				}) {
					t.Error("TryGo submitted to a finished group")
				}
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var (
				mu      sync.Mutex
				reports int
			)

			hooks := async.Hooks{OnTaskComplete: func(_ int, _ string, _, _ time.Duration, _ error) {
				mu.Lock()
				reports++
				mu.Unlock()
			}}

			run, err := async.New(async.Limited(1), async.WithHooks(hooks))
			if err != nil {
				t.Fatal(err)
			}

			eg, _ := run.ErrGroup(context.Background())

			released := make(chan struct{})
			tt.run(t, eg, released)
			// Release before waiting: a case that holds the limit is holding
			// it by blocking on this channel, so joining first would deadlock
			// the test rather than fail it.
			close(released)

			_ = eg.Wait()

			mu.Lock()
			defer mu.Unlock()

			if reports != tt.want {
				t.Fatalf("hook fired %d times, want %d", reports, tt.want)
			}
		})
	}
}
