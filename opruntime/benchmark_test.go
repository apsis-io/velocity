package opruntime_test

import (
	"testing"

	"github.com/apsis-io/velocity/opcodes"
	"github.com/apsis-io/velocity/opruntime"
)

func BenchmarkTable(b *testing.B) {
	b.Run("dispatch", func(b *testing.B) {
		table := opruntime.NewTable()
		_ = table.Register(opDemo, func(opcodes.Instruction) error { return nil })
		inst := opcodes.Instruction{Op: opDemo}
		b.ReportAllocs()
		for b.Loop() {
			_ = table.Dispatch(inst)
		}
	})

	b.Run("run", func(b *testing.B) {
		table := opruntime.NewTable()
		_ = table.Register(opDemo, func(opcodes.Instruction) error { return nil })
		program := make([]opcodes.Instruction, 16)
		for i := range program {
			program[i] = opcodes.Instruction{Op: opDemo}
		}
		b.ReportAllocs()
		for b.Loop() {
			_ = opruntime.Run(program, table)
		}
	})
}
