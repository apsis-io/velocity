package lostrelease_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestVetToolAgainstRealPackages builds velocityvet and runs it through go
// vet over a fixture module compiled against the real ownership and pool
// packages. The analysistest above proves the logic against stubs; this
// proves the tool still resolves the real acquiring methods, which a rename
// in ownership would otherwise break silently.
func TestVetToolAgainstRealPackages(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs go vet")
	}
	tool := filepath.Join(t.TempDir(), "velocityvet")
	build := exec.Command("go", "build", "-o", tool, "../cmd/velocityvet")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build velocityvet: %v\n%s", err, out)
	}

	fixture, err := filepath.Abs(filepath.Join("testdata", "fixture"))
	if err != nil {
		t.Fatal(err)
	}
	vet := exec.Command("go", "vet", "-vettool="+tool, "./...")
	vet.Dir = fixture
	vet.Env = append(os.Environ(), "GOFLAGS=-mod=mod")
	out, err := vet.CombinedOutput()
	if err == nil {
		t.Fatalf("go vet passed; expected the fixture's leaks to be reported:\n%s", out)
	}
	got := string(out)

	for _, want := range []string{
		`fixture\.go:\d+:\d+: borrow returned by ownership\.Owner\.BorrowMut is not released on all paths`,
		`fixture\.go:\d+:\d+: this return statement may be reached without releasing borrow`,
		`fixture\.go:\d+:\d+: c returned by pool\.Pool\.Get is not released on all paths`,
		`fixture\.go:\d+:\d+: the handle returned by ownership\.NewLease should be released, not discarded`,
	} {
		if !regexp.MustCompile(want).MatchString(got) {
			t.Errorf("missing diagnostic %q in:\n%s", want, got)
		}
	}
	// Clean must not be reported: its early return is the acquisition's own
	// error branch and its success path defers Release. It is the only
	// fixture function using plain Borrow.
	if strings.Contains(got, "ownership.Owner.Borrow is not released") {
		t.Errorf("Clean was reported:\n%s", got)
	}
	if n := strings.Count(got, "is not released on all paths"); n != 2 {
		t.Errorf("expected exactly 2 unreleased handles, got %d:\n%s", n, got)
	}
}
