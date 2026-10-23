package opruntime_test

import (
	"testing"

	"github.com/apsis-io/velocity/opcodes"
	"github.com/apsis-io/velocity/opruntime"
)

func BenchmarkTable(b *testing.B) {
	b.Run("direct", func(b *testing.B) {
		handler := func(_ struct{}, _ opcodes.Instruction, in *demoIn, out *demoOut) error {
			out.Y = in.X
			return nil
		}
		inst := opcodes.Instruction{Op: opDemo}
		in := demoIn{X: 1}
		out := demoOut{}

		b.ReportAllocs()

		for b.Loop() {
			_ = handler(struct{}{}, inst, &in, &out)
		}
	})

	b.Run("dispatch", func(b *testing.B) {
		table := opruntime.NewTable[struct{}, demoIn, demoOut]()
		_ = table.Register(opDemo, func(_ struct{}, _ opcodes.Instruction, in *demoIn, out *demoOut) error {
			out.Y = in.X
			return nil
		})
		inst := opcodes.Instruction{Op: opDemo}
		in := demoIn{X: 1}
		out := demoOut{}

		b.ReportAllocs()

		for b.Loop() {
			_ = table.Dispatch(struct{}{}, inst, &in, &out)
		}
	})

	b.Run("run", func(b *testing.B) {
		table := opruntime.NewTable[struct{}, demoIn, demoOut]()
		_ = table.Register(opDemo, func(_ struct{}, _ opcodes.Instruction, in *demoIn, out *demoOut) error {
			out.Y += in.X
			return nil
		})

		program := make([]opcodes.Instruction, 16)
		for i := range program {
			program[i] = opcodes.Instruction{Op: opDemo}
		}

		in := demoIn{X: 1}
		out := demoOut{}

		b.ReportAllocs()

		for b.Loop() {
			_ = opruntime.Run(struct{}{}, program, table, &in, &out)
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
		table := opruntime.NewTable[struct{}, demoIn, demoOut]()
		for _, op := range switchOps {
			_ = table.Register(op, func(_ struct{}, _ opcodes.Instruction, _ *demoIn, out *demoOut) error {
				out.Y += int(op)
				return nil
			})
		}

		in := demoIn{}
		out := demoOut{}

		b.ReportAllocs()

		i := 0
		for b.Loop() {
			_ = table.Dispatch(struct{}{}, opcodes.Instruction{Op: switchOps[i%len(switchOps)]}, &in, &out)
			i++
		}

		dispatchSink = out.Y
	})
}
