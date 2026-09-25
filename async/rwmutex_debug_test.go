//go:build velocitydebug

package async

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// The checks are a net for what Permit.Release's Once absorbs, so the test has
// to reach the counter the way the net exists for: directly, not through
// Release, which by design makes a second call a no-op and so can never trip
// the check. That is the point of both — one prevents the ordinary mistake, the
// other catches what is left.
func TestDebugChecksCatchAnOverRelease(t *testing.T) {
	t.Run("read", func(t *testing.T) {
		m := NewRWMutex()
		// readers is already zero, so a release drives it to -1: the state
		// in which wake is unreachable and a writer is admitted under a live
		// reader.
		assertPanic(t, "released more than once", m.unlockRead)
	})

	t.Run("write", func(t *testing.T) {
		m := NewRWMutex()
		assertPanic(t, "released while none was held", m.unlockWrite)
	})
}

// A legitimate release must not trip either check, or the net would be noise.
func TestDebugChecksPassALegitimateRelease(t *testing.T) {
	ctx := context.Background()

	readers := NewRWMutex()
	for range 3 {
		permit, err := readers.RLock(ctx)
		if err != nil {
			t.Fatal(err)
		}
		permit.Release()
	}

	writer := NewRWMutex()
	held, err := writer.Lock(ctx)
	if err != nil {
		t.Fatal(err)
	}
	held.Release()
}

func assertPanic(t *testing.T, want string, fn func()) {
	t.Helper()

	defer func() {
		recovered := recover()
		if recovered == nil {
			t.Fatalf("no panic, want one containing %q", want)
		}
		if !strings.Contains(fmt.Sprint(recovered), want) {
			t.Fatalf("panic = %v, want it to contain %q", recovered, want)
		}
	}()

	fn()
}
