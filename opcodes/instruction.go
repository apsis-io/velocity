package opcodes

// Instruction pairs an Op with three generic operand slots. The meaning of
// A, B, and C is defined entirely by whatever opruntime.Handler is
// registered for Op, not by this package.
type Instruction struct {
	Op      Op
	A, B, C uint32
}
