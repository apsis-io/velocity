//go:build velocitydebug

package ownership

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

// TestDebugNetLogsItsDrop pins the one thing the debug build adds to the drop
// net: saying out loud that a Drop ran by the net rather than by a release.
// The log path is called directly, as the borrow-leak test does — the net's
// firing itself is asynchronous and covered by the non-debug tests.
func TestDebugNetLogsItsDrop(t *testing.T) {
	var output bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(bufferHandler{buffer: &output}))
	defer slog.SetDefault(old)

	net := &backstop[int]{value: 1, drop: func(int) error { return nil }}
	net.run()

	if got := output.String(); !strings.Contains(got, "unreachable net") {
		t.Fatalf("log = %q, want the net's drop named", got)
	}
}

// A disarmed net runs nothing, so a debug build does not log a drop that the
// explicit release already took care of.
func TestDebugNetDisarmedRunsNothing(t *testing.T) {
	var output bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(bufferHandler{buffer: &output}))
	defer slog.SetDefault(old)

	var dropped int
	net := &backstop[int]{value: 1, drop: func(int) error { dropped++; return nil }}
	net.disarmed = true
	net.run()

	if dropped != 0 {
		t.Fatalf("disarmed net dropped %d times, want none", dropped)
	}

	if got := output.String(); got != "" {
		t.Fatalf("log = %q, want silence from a disarmed net", got)
	}
}
