package opruntime

import "github.com/apsis-io/velocity/opcodes"

// Handler executes a single decoded instruction. A Handler must return
// normally; it must not panic or call runtime.Goexit.
type Handler func(opcodes.Instruction) error

// Table maps an opcodes.Op to the Handler that implements it.
type Table struct {
	handlers map[opcodes.Op]Handler
}

// NewTable creates an empty Table.
func NewTable() *Table {
	return &Table{handlers: make(map[opcodes.Op]Handler)}
}

// Register adds h as the handler for op. Registering a nil handler or a
// second handler for the same op is an error.
func (t *Table) Register(op opcodes.Op, h Handler) error {
	if h == nil {
		return &RegisterError{Op: op, Cause: ErrNilHandler}
	}
	if _, ok := t.handlers[op]; ok {
		return &RegisterError{Op: op, Cause: ErrDuplicateHandler}
	}
	t.handlers[op] = h
	return nil
}

// Handler returns the handler registered for op, if any.
func (t *Table) Handler(op opcodes.Op) (Handler, bool) {
	h, ok := t.handlers[op]
	return h, ok
}
