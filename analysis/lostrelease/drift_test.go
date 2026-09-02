package lostrelease

import (
	"go/types"
	"path/filepath"
	"testing"

	"golang.org/x/tools/go/packages"
)

// notAcquirers are functions and methods that hand back something with a
// Release method but do not acquire it: constructors, whose caller already
// holds the obligation from the moment it asks; transfers, which move an
// obligation that already existed; and conversions between handle kinds.
// Each is listed so that a NEW one has to be classified deliberately rather
// than defaulting to unchecked.
var notAcquirers = map[string]string{
	// Constructors: the caller asked for the resource, so the obligation
	// begins here rather than being handed out mid-expression.
	//
	// pool.New, async.NewSemaphore and async.NewMutex are absent because a
	// Pool, Semaphore and Mutex are not themselves releasable — what they
	// hand out is (Checkout, Permit) — so they never reach this table. The
	// reverse check below caught them listed here from assumption.
	"ownership.New":       "constructor",
	"ownership.Own":       "constructor",
	"ownership.NewCloser": "constructor",
	"ownership.NewShared": "constructor",
	"ownership.NewFrozen": "constructor",

	// Transfers: an obligation that already existed changes hands. The
	// analyzer sees the source handle's own acquisition.
	"ownership.Owner.Move":       "transfer",
	"ownership.Owner.IntoShared": "transfer",
	"ownership.Owner.Freeze":     "transfer",
	"ownership.Owner.Map":        "transfer to a new cell",
	"ownership.Shared.Clone":     "counted handle from one already held",
	"ownership.Shared.IntoOwner": "transfer",
	"ownership.Frozen.Clone":     "counted handle from one already held",
	"ownership.Frozen.IntoOwner": "transfer",
	"ownership.Lease.Move":       "transfer",
	"pool.Checkout.Move":         "transfer",
}

// TestAcquirerTableHasNotDrifted keys on the structure the claim is about —
// a function whose first result is a type with a Release method — rather
// than on the names in the acquirers table, which are chosen by whoever
// writes velocity and cannot be searched for before they exist.
//
// Without this, adding an acquiring method to ownership, pool, or async
// leaves it silently unchecked: the analyzer would keep passing, its tests
// would keep passing, and the gap would show up only as a leak in
// somebody's code. Failing here is the whole point.
func TestAcquirerTableHasNotDrifted(t *testing.T) {
	if testing.Short() {
		t.Skip("loads the velocity packages")
	}
	dir, err := filepath.Abs(filepath.Join("testdata", "fixture"))
	if err != nil {
		t.Fatal(err)
	}
	// The fixture module already requires a published velocity, so this
	// checks the table against a real release rather than hand-written stubs.
	cfg := &packages.Config{Mode: packages.NeedName | packages.NeedTypes, Dir: dir}
	pkgs, err := packages.Load(cfg, ownershipPath, poolPath, asyncPath)
	if err != nil {
		t.Fatal(err)
	}
	loaded := 0
	seen := map[string]bool{}
	for _, pkg := range pkgs {
		if pkg.Types == nil || len(pkg.Errors) > 0 {
			for _, e := range pkg.Errors {
				t.Errorf("loading %s: %v", pkg.PkgPath, e)
			}
			continue
		}
		loaded++
		checkPackage(t, pkg, seen)
	}
	if loaded != 3 {
		t.Fatalf("loaded %d of 3 velocity packages; the check proves nothing if they did not load", loaded)
	}
	// A green result here means nothing unless the scan actually found
	// releasable returns. Both halves of the table must be reachable.
	if len(seen) == 0 {
		t.Fatal("no function returning a releasable handle was found; isReleasable is not matching and the check is vacuous")
	}
	for excluded := range notAcquirers {
		if !seen[excluded] {
			t.Errorf("notAcquirers lists %s, which no longer exists or no longer returns a releasable handle; remove it", excluded)
		}
	}
	for path, byRecv := range acquirers {
		for recv, names := range byRecv {
			for _, name := range names {
				qualified := shortName(path) + "."
				if recv != "" {
					qualified += recv + "."
				}
				if qualified += name; !seen[qualified] {
					t.Errorf("acquirers lists %s, which no longer exists or no longer returns a releasable handle", qualified)
				}
			}
		}
	}
}

// shortName is the package name the qualified keys above are built from.
func shortName(path string) string {
	switch path {
	case ownershipPath:
		return "ownership"
	case poolPath:
		return "pool"
	case asyncPath:
		return "async"
	}
	return path
}

func checkPackage(t *testing.T, pkg *packages.Package, seen map[string]bool) {
	t.Helper()
	scope := pkg.Types.Scope()
	for _, name := range scope.Names() {
		obj := scope.Lookup(name)
		if !obj.Exported() {
			continue
		}
		switch obj := obj.(type) {
		case *types.Func:
			checkCallable(t, pkg, "", obj, seen)
		case *types.TypeName:
			named, ok := types.Unalias(obj.Type()).(*types.Named)
			if !ok {
				continue
			}
			// Methods hang off the pointer type for these handles.
			for sel := range types.NewMethodSet(types.NewPointer(named)).Methods() {
				method, ok := sel.Obj().(*types.Func)
				if ok && method.Exported() {
					checkCallable(t, pkg, named.Obj().Name(), method, seen)
				}
			}
		}
	}
}

// checkCallable reports a function whose first result is releasable and
// which the analyzer neither tracks nor deliberately excludes.
func checkCallable(t *testing.T, pkg *packages.Package, recv string, fn *types.Func, seen map[string]bool) {
	t.Helper()
	sig, ok := fn.Type().(*types.Signature)
	if !ok || sig.Results().Len() == 0 {
		return
	}
	if !isReleasable(sig.Results().At(0).Type()) {
		return
	}
	qualified := pkg.Types.Name() + "."
	if recv != "" {
		qualified += recv + "."
	}
	qualified += fn.Name()
	seen[qualified] = true

	if _, excluded := notAcquirers[qualified]; excluded {
		return
	}
	for _, name := range acquirers[pkg.PkgPath][recv] {
		if name == fn.Name() {
			return
		}
	}
	t.Errorf("%s returns a releasable handle but lostrelease neither tracks it nor excludes it.\n"+
		"\tAdd it to acquirers if a caller must release what it returns, or to notAcquirers with the reason if not.", qualified)
}

// isReleasable reports whether a value of this type carries a Release
// method — the structural mark of something a caller must hand back. The
// compiler answers this; a name search could not, since the next acquirer
// need not be spelled like the current ones.
func isReleasable(t types.Type) bool {
	if named, ok := types.Unalias(t).(*types.Named); ok {
		t = types.NewPointer(named)
	}
	for sel := range types.NewMethodSet(t).Methods() {
		if sel.Obj().Name() == "Release" {
			return true
		}
	}
	return false
}
