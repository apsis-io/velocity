package opruntime_test

import (
	"errors"
	"testing"

	"github.com/apsis-io/velocity/opcodes"
	"github.com/apsis-io/velocity/opruntime"
)

const opDemo opcodes.Op = 1

func TestTableRegister(t *testing.T) {
	t.Run("nil handler", func(t *testing.T) {
		table := opruntime.NewTable()
		err := table.Register(opDemo, nil)
		if !errors.Is(err, opruntime.ErrNilHandler) {
			t.Fatalf("Register(nil) = %v, want ErrNilHandler", err)
		}
	})

	t.Run("duplicate handler", func(t *testing.T) {
		table := opruntime.NewTable()
		noop := func(opcodes.Instruction) error { return nil }
		if err := table.Register(opDemo, noop); err != nil {
			t.Fatalf("first Register: %v", err)
		}
		err := table.Register(opDemo, noop)
		if !errors.Is(err, opruntime.ErrDuplicateHandler) {
			t.Fatalf("second Register = %v, want ErrDuplicateHandler", err)
		}
	})

	t.Run("lookup", func(t *testing.T) {
		table := opruntime.NewTable()
		if _, ok := table.Handler(opDemo); ok {
			t.Fatalf("Handler found before Register")
		}
		called := false
		if err := table.Register(opDemo, func(opcodes.Instruction) error {
			called = true
			return nil
		}); err != nil {
			t.Fatalf("Register: %v", err)
		}
		h, ok := table.Handler(opDemo)
		if !ok {
			t.Fatalf("Handler not found after Register")
		}
		if err := h(opcodes.Instruction{Op: opDemo}); err != nil {
			t.Fatalf("handler returned error: %v", err)
		}
		if !called {
			t.Fatalf("handler was not invoked")
		}
	})
}
