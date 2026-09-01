//go:build velocitydebug

package ownership

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

type bufferHandler struct{ buffer *bytes.Buffer }

func (h bufferHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h bufferHandler) Handle(_ context.Context, record slog.Record) error {
	h.buffer.WriteString(record.Message)
	record.Attrs(func(attr slog.Attr) bool {
		h.buffer.WriteByte(' ')
		h.buffer.WriteString(attr.Key)
		return true
	})
	return nil
}
func (h bufferHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h bufferHandler) WithGroup(string) slog.Handler      { return h }

func TestDebugCleanupLogsLeak(t *testing.T) {
	var output bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(bufferHandler{buffer: &output}))
	defer slog.SetDefault(old)

	owner, err := New(1)
	if err != nil {
		t.Fatal(err)
	}
	borrow, err := owner.Borrow()
	if err != nil {
		t.Fatal(err)
	}
	cleanupLease(borrow.lease)
	if got := output.String(); !strings.Contains(got, "borrow leaked") || !strings.Contains(got, "borrow_id") {
		t.Fatalf("log = %q", got)
	}
}

func TestDebugCleanupReleasesBorrow(t *testing.T) {
	owner, err := New(1)
	if err != nil {
		t.Fatal(err)
	}
	borrow, err := owner.Borrow()
	if err != nil {
		t.Fatal(err)
	}
	cleanupLease(borrow.lease)
	if state := owner.State(); state.Readers != 0 {
		t.Fatalf("state = %+v", state)
	}
	if err := borrow.Release(); err != nil {
		t.Fatal(err)
	}
	if err := owner.Release(); err != nil {
		t.Fatal(err)
	}
}
