package velocity_test

import (
	"testing"

	"github.com/apsis-io/velocity"
)

func TestVersion(t *testing.T) {
	if velocity.Version != "dev" {
		t.Fatalf("Version = %q, want dev", velocity.Version)
	}
}
