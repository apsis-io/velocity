package opruntime_test

import (
	"errors"
	"testing"

	"github.com/apsis-io/velocity/opcodes"
	"github.com/apsis-io/velocity/opruntime"
)

func TestTableDispatch(t *testing.T) {
	t.Run("no handler", func(t *testing.T) {
		table := opruntime.NewTable[struct{}, demoIn, demoOut]()
		err := table.Dispatch(struct{}{}, opcodes.Instruction{Op: opDemo}, &demoIn{}, &demoOut{})

		var dispatchErr *opruntime.DispatchError
		if !errors.As(err, &dispatchErr) || !errors.Is(err, opruntime.ErrNoHandler) {
			t.Fatalf("Dispatch = %v, want DispatchError wrapping ErrNoHandler", err)
		}

		if dispatchErr.Op != opDemo {
			t.Errorf("DispatchError.Op = %v, want %v", dispatchErr.Op, opDemo)
		}
	})

	t.Run("handler error", func(t *testing.T) {
		table := opruntime.NewTable[struct{}, demoIn, demoOut]()
		wantErr := errors.New("boom")

		if err := table.Register(opDemo, func(struct{}, opcodes.Instruction, *demoIn, *demoOut) error { return wantErr }); err != nil {
			t.Fatalf("Register: %v", err)
		}

		err := table.Dispatch(struct{}{}, opcodes.Instruction{Op: opDemo}, &demoIn{}, &demoOut{})
		if !errors.Is(err, wantErr) {
			t.Fatalf("Dispatch = %v, want wrapping %v", err, wantErr)
		}
	})

	t.Run("success", func(t *testing.T) {
		table := opruntime.NewTable[struct{}, demoIn, demoOut]()

		var got opcodes.Instruction

		if err := table.Register(opDemo, func(_ struct{}, inst opcodes.Instruction, in *demoIn, out *demoOut) error {
			got = inst
			out.Y = in.X + int(inst.A)
			return nil
		}); err != nil {
			t.Fatalf("Register: %v", err)
		}

		in := demoIn{X: 41}
		out := demoOut{}
		want := opcodes.Instruction{Op: opDemo, A: 1, B: 2, C: 3}

		if err := table.Dispatch(struct{}{}, want, &in, &out); err != nil {
			t.Fatalf("Dispatch: %v", err)
		}

		if got != want {
			t.Errorf("handler received %+v, want %+v", got, want)
		}

		if out.Y != 42 {
			t.Errorf("out.Y = %d, want 42", out.Y)
		}
	})
}
