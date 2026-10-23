package opruntime

import "github.com/apsis-io/velocity/opcodes"

// Run dispatches every instruction in program against (ctx, in, out), in
// order, stopping at the first error. It is a thin convenience loop around
// Table.Dispatch.
func Run[Ctx, In, Out any](ctx Ctx, program []opcodes.Instruction, table *Table[Ctx, In, Out], in *In, out *Out) error {
	for i, inst := range program {
		if err := table.Dispatch(ctx, inst, in, out); err != nil {
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
