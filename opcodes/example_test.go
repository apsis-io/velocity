package opcodes_test

import (
	"fmt"

	"github.com/apsis-io/velocity/opcodes"
)

func ExampleOp_String() {
	const opIncrement opcodes.Op = 1

	fmt.Println(opcodes.OpNop)
	fmt.Println(opIncrement)
	// Output:
	// nop
	// Op(1)
}
