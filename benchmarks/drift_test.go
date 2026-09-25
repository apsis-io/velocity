package benchmarks_test

import (
	"bufio"
	"context"
	"math"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The numbers in benchmarks/README.md are hand-maintained, which is why a code
// change in another module can silently invalidate them: three claims were
// stale on 2026-09-25 and none of us knew, because nothing failed - the table
// just stopped describing the code. This file is the guard for that.
//
// It runs the benchmarks the README publishes, takes the median of three
// samples, and fails when the published median for a named benchmark is off
// by more than driftTolerance.
//
// SCOPE, deliberately narrow: the published table says "Indicative only -
// measured on one machine, not authoritative", so this is a DRIFT guard and
// not a regression suite. It answers "does the table still describe this
// code", never "is this code faster than that one".

// driftTolerance is the band a published number must fall in to count as
// still describing the code. 15% is well inside the run-to-run noise of the
// median on an otherwise idle machine and well outside the ~31% the stale Map
// figure was found at; if a real regression lands inside this band, the
// benchmark comparison tables are the wrong instrument to catch it anyway.
//
// A constant with this comment rather than a magic number, so that loosening
// it later has to say why.
const driftTolerance = 0.15

// driftChecks: the published figures, transcribed from benchmarks/README.md.
// THE CONSTANTS ARE A COPY, and the copy is the weak half: nothing here fails
// when the README changes and this file does not. The failure mode is bounded
// - a stale constant makes the guard compare against yesterday's table - but
// it is real. The remedy is procedural and costs one line: any commit that
// re-measures the table updates these constants in the same commit.
var driftChecks = []struct {
	benchmark string
	subkey    string
	nsop      float64
}{
	// Names verified against the harness's own output, not against the README
	// rows - BenchmarkAsyncGatherVelocityLimited is its own top-level
	// benchmark, and the errgroup Gather arm's subkey is longer than the row
	// text suggests. A name invented from the table would have matched nothing
	// and read as drift.
	{"BenchmarkAsyncGather", "velocity/unlimited", 4443},
	{"BenchmarkAsyncGatherVelocityLimited", "", 6123},
	{"BenchmarkAsyncGather", "errgroup/preallocated-source-order", 3467},
	{"BenchmarkAsyncMap", "velocity/map/8", 3991},
	{"BenchmarkAsyncMap", "velocity/map/1024", 42872},
	{"BenchmarkErrGroup", "velocity/unlimited", 3386},
}

// TestPublishedNumbersHaveNotDrifted re-runs the benchmarks the README
// publishes and compares each median against the constant above.
//
// Skipped under -short, and skipped when a go build of the module fails: a
// tree that does not build produces no numbers, and instrument-broken must
// not read as drift.
func TestPublishedNumbersHaveNotDrifted(t *testing.T) {
	if testing.Short() {
		t.Skip("drift check runs the real benchmarks; -short skips it")
	}

	if out, err := exec.Command("go", "build", "./...").CombinedOutput(); err != nil {
		t.Fatalf("the module does not build, so a drift reading would be fiction: %v\n%s", err, out)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	for _, dc := range driftChecks {
		dc := dc
		t.Run(dc.benchmark+"/"+dc.subkey, func(t *testing.T) {
			median := runBenchmarkMedian(t, ctx, dc.benchmark, dc.subkey)
			if median <= 0 {
				t.Fatalf("no measurement came back for %s/%s - the filter matched nothing, which is instrument-broken, not a pass", dc.benchmark, dc.subkey)
			}

			delta := math.Abs(median-dc.nsop) / dc.nsop
			if delta > driftTolerance {
				t.Fatalf("%s/%s now measures %.0f ns/op against a published %.0f - %.0f%% drift, past the %.0f%% band. Re-measure (go test -bench . -benchmem -count=5) and update the README and these constants in the same commit.",
					dc.benchmark, dc.subkey, median, dc.nsop, delta*100, driftTolerance*100)
			}
		})
	}
}

// runBenchmarkMedian runs one benchmark filtered to one sub-key and returns
// the median ns/op of three samples at a small fixed benchtime.
func runBenchmarkMedian(t *testing.T, ctx context.Context, benchmark, subkey string) float64 {
	t.Helper()

	// The pattern carries a $ anchor, so QuoteMeta is WRONG here - it would
	// escape the anchor into a literal and the filter would match nothing,
	// which is the instrument-broken arm this test refuses to read as drift.
	// The names below are literals from this package; nothing needs quoting.
	benchPattern := benchmark + "/" + subkey + "$"
	if subkey == "" {
		benchPattern = benchmark + "$"
	}

	cmd := exec.CommandContext(ctx, "go", "test", "./...", "-run", "^$",
		"-bench", benchPattern,
		"-benchtime", "500x", "-count", "3")

	cmd.Env = os.Environ()

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("benchmark run failed (instrument-broken is not drift): %v\n%s", err, out)
	}

	var samples []float64

	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.Contains(line, subkey) || !strings.Contains(line, "ns/op") {
			continue
		}

		fields := strings.Fields(line)
		for i, f := range fields {
			if f == "ns/op" && i > 0 {
				v, convErr := strconv.ParseFloat(fields[i-1], 64)
				if convErr != nil {
					t.Fatalf("unparseable ns/op %q in %q - reading the instrument, not the code", fields[i-1], line)
				}

				samples = append(samples, v)
			}
		}
	}

	if len(samples) == 0 {
		t.Fatalf("the benchmark ran but printed no ns/op line for %s - the subkey or filter is wrong, which is instrument-broken, not a pass", subkey)
	}

	for i := 1; i < len(samples); i++ {
		for j := i; j > 0 && samples[j] < samples[j-1]; j-- {
			samples[j], samples[j-1] = samples[j-1], samples[j]
		}
	}

	return samples[len(samples)/2]
}
