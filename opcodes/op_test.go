package opcodes_test

import (
	"testing"

	"github.com/apsis-io/velocity/opcodes"
)

func TestOpString(t *testing.T) {
	tests := []struct {
		name string
		op   opcodes.Op
		want string
	}{
		{"nop", opcodes.OpNop, "nop"},
		{"one", opcodes.Op(1), "Op(1)"},
		{"max", opcodes.Op(255), "Op(255)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.op.String(); got != tt.want {
				t.Errorf("Op(%d).String() = %q, want %q", tt.op, got, tt.want)
			}
		})
	}
}
