package opruntime

import "github.com/apsis-io/velocity/opcodes"

// Handler executes a single decoded instruction against an execution context,
// an input frame struct (*In), and a destination output frame struct (*Out).
// A Handler must return normally; it must not panic or call runtime.Goexit.
type Handler[Ctx, In, Out any] func(ctx Ctx, inst opcodes.Instruction, in *In, out *Out) error

// Table maps an opcodes.Op to the Handler that implements it. opcodes.Op is
// a uint8, so lookup is a direct array index rather than a hash lookup.
type Table[Ctx, In, Out any] struct {
	handlers [256]Handler[Ctx, In, Out]
}

// NewTable creates an empty Table for the given (Ctx, In, Out) frame types.
func NewTable[Ctx, In, Out any]() *Table[Ctx, In, Out] {
	return &Table[Ctx, In, Out]{}
}

// Register adds h as the handler for op. Registering a nil handler or a
// second handler for the same op is an error.
func (t *Table[Ctx, In, Out]) Register(op opcodes.Op, h Handler[Ctx, In, Out]) error {
	if h == nil {
		return &RegisterError{Op: op, Cause: ErrNilHandler}
	}

	if t.handlers[op] != nil {
		return &RegisterError{Op: op, Cause: ErrDuplicateHandler}
	}

	t.handlers[op] = h

	return nil
}

// RegisterAtom adapts a pure struct-frame AtomFn[Ctx, In, Out] (which does
// not inspect instruction operands) and registers it as the handler for op.
func (t *Table[Ctx, In, Out]) RegisterAtom(op opcodes.Op, atom AtomFn[Ctx, In, Out]) error {
	if atom == nil {
		return &RegisterError{Op: op, Cause: ErrNilHandler}
	}

	return t.Register(op, func(ctx Ctx, _ opcodes.Instruction, in *In, out *Out) error {
		return atom(ctx, in, out)
	})
}

// Handler returns the handler registered for op, if any.
func (t *Table[Ctx, In, Out]) Handler(op opcodes.Op) (Handler[Ctx, In, Out], bool) {
	h := t.handlers[op]
	return h, h != nil
}
