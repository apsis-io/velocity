package opruntime

import "github.com/apsis-io/velocity/opcodes"

// Run dispatches every instruction in program, in order, stopping at the
// first error. It is a thin convenience loop around Table.Dispatch.
func Run(program []opcodes.Instruction, table *Table) error {
	for i, inst := range program {
		if err := table.Dispatch(inst); err != nil {
			var dispatchErr *DispatchError
			if de, ok := err.(*DispatchError); ok {
				dispatchErr = de
			} else {
				dispatchErr = &DispatchError{Index: -1, Op: inst.Op, Cause: err}
			}
			dispatchErr.Index = i
			return dispatchErr
		}
	}
	return nil
}
