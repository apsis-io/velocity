package opcodes

import "strconv"

// Op identifies an operation. The zero value, OpNop, is always a safe no-op.
// Every other value is defined by the domain package that uses it.
type Op uint8

// OpNop is the only predefined Op. It is the zero value of Op.
const OpNop Op = 0

// String returns "nop" for OpNop and "Op(N)" for any other value, since this
// package does not know the names domain packages give their own operations.
func (o Op) String() string {
	if o == OpNop {
		return "nop"
	}
	return "Op(" + strconv.FormatUint(uint64(o), 10) + ")"
}
