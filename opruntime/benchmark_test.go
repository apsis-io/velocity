package opruntime_test

import (
	"testing"

	"github.com/apsis-io/velocity/opcodes"
	"github.com/apsis-io/velocity/opruntime"
)

func BenchmarkTable(b *testing.B) {
	b.Run("direct", func(b *testing.B) {
		handler := func(opcodes.Instruction) error { return nil }
		inst := opcodes.Instruction{Op: opDemo}
		b.ReportAllocs()
		for b.Loop() {
			_ = handler(inst)
		}
	})

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

// switchOps mirrors the shape of a hand-written op-dispatch switch, such as
// ownership/model_test.go's FuzzOwnershipModel switching on op%10: a small
// fixed set of operations, each doing a little inline work, selected by op
// code. It's used to compare a real switch against opruntime.Table.Dispatch
// for the same shape of problem, not just a single always-hit op.
var switchOps = []opcodes.Op{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}

var dispatchSink int

func BenchmarkSwitchVsTable(b *testing.B) {
	b.Run("switch", func(b *testing.B) {
		b.ReportAllocs()
		i := 0
		for b.Loop() {
			switch switchOps[i%len(switchOps)] {
			case 1:
				dispatchSink += 1
			case 2:
				dispatchSink += 2
			case 3:
				dispatchSink += 3
			case 4:
				dispatchSink += 4
			case 5:
				dispatchSink += 5
			case 6:
				dispatchSink += 6
			case 7:
				dispatchSink += 7
			case 8:
				dispatchSink += 8
			case 9:
				dispatchSink += 9
			case 10:
				dispatchSink += 10
			}
			i++
		}
	})

	b.Run("table", func(b *testing.B) {
		table := opruntime.NewTable()
		for _, op := range switchOps {
			_ = table.Register(op, func(opcodes.Instruction) error {
				dispatchSink += int(op)
				return nil
			})
		}
		b.ReportAllocs()
		i := 0
		for b.Loop() {
			_ = table.Dispatch(opcodes.Instruction{Op: switchOps[i%len(switchOps)]})
			i++
		}
	})
}
