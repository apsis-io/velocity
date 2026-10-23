package opruntime_test

import (
	"errors"
	"testing"

	"github.com/apsis-io/velocity/opcodes"
	"github.com/apsis-io/velocity/opruntime"
)

const (
	opIncrement opcodes.Op = 1
	opFail      opcodes.Op = 2
)

func TestRun(t *testing.T) {
	t.Run("sequential success", func(t *testing.T) {
		table := opruntime.NewTable[struct{}, demoIn, demoOut]()

		if err := table.Register(opIncrement, func(_ struct{}, inst opcodes.Instruction, in *demoIn, out *demoOut) error {
			out.Y += in.X * int(inst.A)
			return nil
		}); err != nil {
			t.Fatalf("Register: %v", err)
		}

		program := []opcodes.Instruction{
			{Op: opIncrement, A: 1},
			{Op: opIncrement, A: 2},
			{Op: opIncrement, A: 3},
		}

		in := demoIn{X: 1}
		out := demoOut{}

		if err := opruntime.Run(struct{}{}, program, table, &in, &out); err != nil {
			t.Fatalf("Run: %v", err)
		}

		if out.Y != 6 {
			t.Errorf("out.Y = %d, want 6", out.Y)
		}
	})

	t.Run("stops at first error", func(t *testing.T) {
		table := opruntime.NewTable[struct{}, demoIn, demoOut]()

		var ran []int

		wantErr := errors.New("boom")

		if err := table.Register(opIncrement, func(struct{}, opcodes.Instruction, *demoIn, *demoOut) error {
			ran = append(ran, 1)
			return nil
		}); err != nil {
			t.Fatalf("Register: %v", err)
		}

		if err := table.Register(opFail, func(struct{}, opcodes.Instruction, *demoIn, *demoOut) error {
			return wantErr
		}); err != nil {
			t.Fatalf("Register: %v", err)
		}

		program := []opcodes.Instruction{
			{Op: opIncrement},
			{Op: opFail},
			{Op: opIncrement},
		}

		err := opruntime.Run(struct{}{}, program, table, &demoIn{}, &demoOut{})
		if !errors.Is(err, wantErr) {
			t.Fatalf("Run = %v, want wrapping %v", err, wantErr)
		}

		var dispatchErr *opruntime.DispatchError
		if !errors.As(err, &dispatchErr) {
			t.Fatalf("Run error is not a *DispatchError: %v", err)
		}

		if dispatchErr.Index != 1 {
			t.Errorf("DispatchError.Index = %d, want 1", dispatchErr.Index)
		}

		if dispatchErr.Op != opFail {
			t.Errorf("DispatchError.Op = %v, want %v", dispatchErr.Op, opFail)
		}

		if len(ran) != 1 {
			t.Errorf("ran = %v, want a single opIncrement before the failure", ran)
		}
	})
}
