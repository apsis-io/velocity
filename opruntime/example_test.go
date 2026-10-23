package opruntime_test

import (
	"fmt"

	"github.com/apsis-io/velocity/opcodes"
	"github.com/apsis-io/velocity/opruntime"
)

func ExampleTable_Dispatch() {
	const opPrint opcodes.Op = 1

	type inFrame struct {
		Base uint32
	}

	type outFrame struct {
		Sum uint32
	}

	table := opruntime.NewTable[struct{}, inFrame, outFrame]()
	_ = table.Register(opPrint, func(_ struct{}, inst opcodes.Instruction, in *inFrame, out *outFrame) error {
		out.Sum = in.Base + inst.A
		fmt.Println("printed", out.Sum)
		return nil
	})

	in := inFrame{Base: 40}
	out := outFrame{}
	_ = table.Dispatch(struct{}{}, opcodes.Instruction{Op: opPrint, A: 2}, &in, &out)
	// Output: printed 42
}
