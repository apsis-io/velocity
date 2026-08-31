package opruntime

import "github.com/apsis-io/velocity/opcodes"

// Dispatch looks up the handler registered for inst.Op and calls it. It
// returns a DispatchError if no handler is registered or if the handler
// itself returns an error.
func (t *Table) Dispatch(inst opcodes.Instruction) error {
	h, ok := t.Handler(inst.Op)
	if !ok {
		return &DispatchError{Index: -1, Op: inst.Op, Cause: ErrNoHandler}
	}
	if err := h(inst); err != nil {
		return &DispatchError{Index: -1, Op: inst.Op, Cause: err}
	}
	return nil
}
