package opruntime_test

import (
	"errors"
	"testing"

	"github.com/apsis-io/velocity/opcodes"
	"github.com/apsis-io/velocity/opruntime"
)

const opDemo opcodes.Op = 1

type demoIn struct {
	X int
}

type demoOut struct {
	Y int
}

func TestTableRegister(t *testing.T) {
	t.Run("nil handler", func(t *testing.T) {
		table := opruntime.NewTable[struct{}, demoIn, demoOut]()

		err := table.Register(opDemo, nil)
		if !errors.Is(err, opruntime.ErrNilHandler) {
			t.Fatalf("Register(nil) = %v, want ErrNilHandler", err)
		}
	})

	t.Run("duplicate handler", func(t *testing.T) {
		table := opruntime.NewTable[struct{}, demoIn, demoOut]()

		noop := func(struct{}, opcodes.Instruction, *demoIn, *demoOut) error { return nil }
		if err := table.Register(opDemo, noop); err != nil {
			t.Fatalf("first Register: %v", err)
		}

		err := table.Register(opDemo, noop)
		if !errors.Is(err, opruntime.ErrDuplicateHandler) {
			t.Fatalf("second Register = %v, want ErrDuplicateHandler", err)
		}
	})

	t.Run("lookup", func(t *testing.T) {
		table := opruntime.NewTable[struct{}, demoIn, demoOut]()
		if _, ok := table.Handler(opDemo); ok {
			t.Fatalf("Handler found before Register")
		}

		if err := table.Register(opDemo, func(_ struct{}, _ opcodes.Instruction, in *demoIn, out *demoOut) error {
			out.Y = in.X * 2
			return nil
		}); err != nil {
			t.Fatalf("Register: %v", err)
		}

		h, ok := table.Handler(opDemo)
		if !ok {
			t.Fatalf("Handler not found after Register")
		}

		in := demoIn{X: 21}
		out := demoOut{}

		if err := h(struct{}{}, opcodes.Instruction{Op: opDemo}, &in, &out); err != nil {
			t.Fatalf("handler returned error: %v", err)
		}

		if out.Y != 42 {
			t.Fatalf("out.Y = %d, want 42", out.Y)
		}
	})

	t.Run("atom bind and call", func(t *testing.T) {
		raw := opruntime.BindAtom(func(_ struct{}, in *demoIn, out *demoOut) error {
			out.Y = in.X + 10
			return nil
		})

		in := demoIn{X: 32}
		out := demoOut{}

		if err := opruntime.CallAtom(raw, struct{}{}, &in, &out); err != nil {
			t.Fatalf("CallAtom: %v", err)
		}

		if out.Y != 42 {
			t.Fatalf("out.Y = %d, want 42", out.Y)
		}
	})
}
