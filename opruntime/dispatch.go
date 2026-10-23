package opruntime

import "github.com/apsis-io/velocity/opcodes"

// Dispatch looks up the handler registered for inst.Op and calls it with
// (ctx, inst, in, out). It returns a DispatchError if no handler is
// registered or if the handler itself returns an error.
func (t *Table[Ctx, In, Out]) Dispatch(ctx Ctx, inst opcodes.Instruction, in *In, out *Out) error {
	h, ok := t.Handler(inst.Op)
	if !ok {
		return newDispatchError(inst.Op, ErrNoHandler)
	}

	if err := h(ctx, inst, in, out); err != nil {
		return newDispatchError(inst.Op, err)
	}

	return nil
}

// newDispatchError is split out of Dispatch, and marked noinline, so Dispatch
// itself stays under the compiler's inlining budget: the cold error path
// becomes a real call instead of an inlined struct literal that the inliner
// would otherwise fold back into Dispatch's own cost.
//
//go:noinline
func newDispatchError(op opcodes.Op, cause error) error {
	return &DispatchError{Index: -1, Op: op, Cause: cause}
}
