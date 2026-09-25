package async_test

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/apsis-io/velocity/async"
)

// The Hooks doc is a promise about what gets reported, and nothing tied that
// promise to the code: a new call site could be added, or the reporting for an
// existing one changed, and the only symptom would be a caller's histogram
// quietly disagreeing with the documentation. That is the failure the
// lostrelease drift test exists to catch, aimed at behaviour rather than at a
// table of names.
//
// The guard below counts CALL SITES, discovered by parsing the package, and it
// started life counting METHODS through reflection. That was wrong twice over,
// and both errors are worth recording because the second is not obvious.
//
// First: reflect.Methods returns only exported methods, so a method-based guard
// is blind to every unexported call site — and two of the four on the ErrGroup
// path are unexported, in skipped and exec.
//
// Second, and the one that mattered: a method is not an event. TryGo contains
// no call to the hook at all. It reaches the hook through Go on one path,
// through skipped on a second, and through start to exec on a third, so a
// table of submission paths claiming "TryGo reports" was a claim about a
// delegation reached three ways rather than a fact about TryGo. The population
// the contract actually describes is the places the hook fires, and those are
// countable by parsing. This was found by being shown the same shape of guard
// in another package, which is a cheaper way to learn a guard is keyed on the
// wrong noun than to reason about it for another week.
//
// The count table at the bottom is the behavioural half and pins the ErrGroup
// group only; the task half is pinned by the tests in gather.go and
// collection.go, which already assert per-task reporting.

// expectedFiringSites is every function in this package that invokes
// Hooks.OnTaskComplete. A new one fails the guard below, and so does an entry
// here that no longer invokes it — a stale row is not a harmless leftover, it
// is a false claim of coverage in the file whose whole job is to be the
// coverage.
//
// The set is the package, not one operation, because Hooks is a Runner-level
// hook and the contract in hooks.go covers tasks as well as submissions. Two
// groups, and the behavioural count table below pins only the second:
//
//	ErrGroup's four  Go and GoContext report a submission that ran or was
//	                 discarded; skipped and exec are unexported and report the
//	                 two halves of that — one that never ran, one that did.
//	tasks            Gather and Map report each task, race reports each
//	                 completion, and cancelRemaining reports the ones Gather
//	                 never claimed.
//
// ForEachFuncs, ForEach and FirstSuccess are deliberately absent: they delegate
// to Gather, Map and race and introduce no new event, which is the distinction a
// method table could not draw. TryGo is absent for the same reason and a
// sharper one — it contains no call at all, reaching the hook through Go on one
// path, skipped on a second and start to exec on a third. A table of submission
// paths would have had to claim "TryGo reports" about a method that never calls
// it, which is a claim about a delegation reached three ways, not about TryGo.
var expectedFiringSites = map[string]bool{
	// ErrGroup's two, both unexported. A submission that ran is reported from
	// exec, in the function's own goroutine; one that never ran is reported
	// from skipped, on the caller's.
	"skipped": true,
	"exec":    true,
	// The task half. Gather and Map report each task, race reports each
	// completion, and cancelRemaining reports the items Gather never claimed.
	"Gather":          true,
	"Map":             true,
	"race":            true,
	"cancelRemaining": true,
}

// Deliberately absent, and the reason the population is sites and not methods:
//
//	Go, GoContext, TryGo   none of them invokes the hook. Go and GoContext each
//	                       reach exec through start, and TryGo reaches Go on
//	                       one path, skipped on a second and start on a third.
//	                       Three hops for the common route.
//	ForEachFuncs, ForEach  delegate to Gather and Map.
//	Race, FirstSuccess     delegate to race.
//	start                  delegates to exec.
//
// Counting sites is also what makes the nil-check from which of these had to be
// excluded: Go and GoContext each test `hooks.OnTaskComplete != nil` before
// queueing a permit, and reading a mention as a call would have put two
// functions in this table that never fire it. A guard is supposed to be the
// thing that does not make that mistake, and the first version of this one
// nearly did.
// firingSites returns the functions in this package that invoke the hook, by
// parsing the source rather than reflecting over the type.
//
// It parses because the population is call SITES, not methods, and two of the
// four are in unexported functions — reflect.Methods sees only exported ones,
// so a method-based guard cannot see them at all. Nor could it tell a method
// that invokes the hook from one that delegates to a function that does: TryGo
// contains no call, and reaches the hook through Go on one path, through
// skipped on another, and through start to exec on the third. A table of
// submission paths therefore said "TryGo reports" about a method that never
// calls it, which is not a fact about TryGo but a claim about a delegation
// reached three ways. Counting sites is the population the contract in
// hooks.go actually describes.
//
// A new submission path that delegates to an existing site is deliberately NOT
// a new entry: it introduces no new event, and the contract is about events. A
// new one that fires the hook itself has to say so.
func firingSites(t *testing.T) map[string]int {
	t.Helper()

	// The test runs with the package directory as its working directory, so
	// this is the package under test and not the external test package.
	pkgs, err := parser.ParseDir(token.NewFileSet(), ".", nil, 0)
	if err != nil {
		t.Fatal(err)
	}

	sites := map[string]int{}
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			if strings.HasSuffix(file.Name.Name, "_test.go") {
				continue
			}

			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}

				if callsHook(fn.Body) {
					sites[fn.Name.Name]++
				}
			}
		}
	}

	return sites
}

// callsHook reports whether a function body invokes the hook, directly or
// through a local alias. exec binds `hook := g.run.hooks.OnTaskComplete` and
// then calls hook, so a selector-only match would miss half the population.
func callsHook(body *ast.BlockStmt) bool {
	aliased := map[string]bool{}

	// Two passes, because a binding can follow the call it enables. Binding a
	// selector-valued name is the only way the hook is called indirectly here,
	// and every such binding is in the same function.
	ast.Inspect(body, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}

		for i, rhs := range assign.Rhs {
			sel, ok := rhs.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "OnTaskComplete" || i >= len(assign.Lhs) {
				continue
			}

			if name, ok := assign.Lhs[i].(*ast.Ident); ok {
				aliased[name.Name] = true
			}
		}

		return true
	})

	calls := false
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}

		switch fun := call.Fun.(type) {
		case *ast.SelectorExpr:
			if fun.Sel.Name == "OnTaskComplete" {
				calls = true
			}
		case *ast.Ident:
			if aliased[fun.Name] {
				calls = true
			}
		}

		return true
	})

	return calls
}

// TestErrGroupFiringSitesAreSpecified fails when the hook gains a call site
// that this table does not describe, and when a site here has gone. Both
// directions: the first is how a new event arrives unannounced, the second is
// how a stale row hides a rename and keeps reading as coverage.
func TestErrGroupFiringSitesAreSpecified(t *testing.T) {
	sites := firingSites(t)

	for name := range sites {
		if !expectedFiringSites[name] {
			t.Errorf("%s invokes Hooks.OnTaskComplete but is not in expectedFiringSites.\n"+
				"\tAdd it and a case to TestErrGroupHookCountsMatchTheContract, or say in the\n"+
				"\tHooks doc why invoking the hook there is not a new event.", name)
		}
	}

	for name := range expectedFiringSites {
		if sites[name] == 0 {
			t.Errorf("expectedFiringSites lists %s, which no longer invokes the hook.\n"+
				"\tRemove it, or the contract describes an event that no longer happens.", name)
		}
	}

	if len(sites) == 0 {
		t.Fatal("no firing sites found; the guard would pass vacuously")
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
