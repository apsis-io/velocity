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
		table := opruntime.NewTable()
		total := 0
		if err := table.Register(opIncrement, func(inst opcodes.Instruction) error {
			total += int(inst.A)
			return nil
		}); err != nil {
			t.Fatalf("Register: %v", err)
		}
		program := []opcodes.Instruction{
			{Op: opIncrement, A: 1},
			{Op: opIncrement, A: 2},
			{Op: opIncrement, A: 3},
		}
		if err := opruntime.Run(program, table); err != nil {
			t.Fatalf("Run: %v", err)
		}
		if total != 6 {
			t.Errorf("total = %d, want 6", total)
		}
	})

	t.Run("stops at first error", func(t *testing.T) {
		table := opruntime.NewTable()
		var ran []int
		wantErr := errors.New("boom")
		if err := table.Register(opIncrement, func(opcodes.Instruction) error {
			ran = append(ran, 1)
			return nil
		}); err != nil {
			t.Fatalf("Register: %v", err)
		}
		if err := table.Register(opFail, func(opcodes.Instruction) error {
			return wantErr
		}); err != nil {
			t.Fatalf("Register: %v", err)
		}
		program := []opcodes.Instruction{
			{Op: opIncrement},
			{Op: opFail},
			{Op: opIncrement},
		}
		err := opruntime.Run(program, table)
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
