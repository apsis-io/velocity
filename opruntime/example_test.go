package opruntime_test

import (
	"fmt"

	"github.com/apsis-io/velocity/opcodes"
	"github.com/apsis-io/velocity/opruntime"
)

func ExampleTable_Dispatch() {
	const opPrint opcodes.Op = 1

	table := opruntime.NewTable()
	_ = table.Register(opPrint, func(inst opcodes.Instruction) error {
		fmt.Println("printed", inst.A)
		return nil
	})

	_ = table.Dispatch(opcodes.Instruction{Op: opPrint, A: 42})
	// Output: printed 42
}
