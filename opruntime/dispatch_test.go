package opruntime_test

import (
	"errors"
	"testing"

	"github.com/apsis-io/velocity/opcodes"
	"github.com/apsis-io/velocity/opruntime"
)

func TestTableDispatch(t *testing.T) {
	t.Run("no handler", func(t *testing.T) {
		table := opruntime.NewTable()
		err := table.Dispatch(opcodes.Instruction{Op: opDemo})
		var dispatchErr *opruntime.DispatchError
		if !errors.As(err, &dispatchErr) || !errors.Is(err, opruntime.ErrNoHandler) {
			t.Fatalf("Dispatch = %v, want DispatchError wrapping ErrNoHandler", err)
		}
		if dispatchErr.Op != opDemo {
			t.Errorf("DispatchError.Op = %v, want %v", dispatchErr.Op, opDemo)
		}
	})

	t.Run("handler error", func(t *testing.T) {
		table := opruntime.NewTable()
		wantErr := errors.New("boom")
		if err := table.Register(opDemo, func(opcodes.Instruction) error { return wantErr }); err != nil {
			t.Fatalf("Register: %v", err)
		}
		err := table.Dispatch(opcodes.Instruction{Op: opDemo})
		if !errors.Is(err, wantErr) {
			t.Fatalf("Dispatch = %v, want wrapping %v", err, wantErr)
		}
	})

	t.Run("success", func(t *testing.T) {
		table := opruntime.NewTable()
		var got opcodes.Instruction
		if err := table.Register(opDemo, func(inst opcodes.Instruction) error {
			got = inst
			return nil
		}); err != nil {
			t.Fatalf("Register: %v", err)
		}
		want := opcodes.Instruction{Op: opDemo, A: 1, B: 2, C: 3}
		if err := table.Dispatch(want); err != nil {
			t.Fatalf("Dispatch: %v", err)
		}
		if got != want {
			t.Errorf("handler received %+v, want %+v", got, want)
		}
	})
}
