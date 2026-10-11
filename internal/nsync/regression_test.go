package nsync

import (
	"testing"
)

func mustPanic(t *testing.T, f func()) {
	t.Helper()

	defer func() {
		if recover() == nil {
			t.Error("expected a panic")
		}
	}()

	f()
}
