package opruntime

import (
	"errors"
	"fmt"

	"github.com/apsis-io/velocity/opcodes"
)

var (
	ErrNilHandler       = errors.New("nil handler")
	ErrDuplicateHandler = errors.New("duplicate handler")
	ErrNoHandler        = errors.New("no handler registered")
)

// RegisterError identifies a Table.Register call that could not be applied.
type RegisterError struct {
	Op    opcodes.Op
	Cause error
}

func (e *RegisterError) Error() string {
	return fmt.Sprintf("register %v: %v", e.Op, e.Cause)
}

func (e *RegisterError) Unwrap() error { return e.Cause }

// DispatchError identifies an instruction that failed to dispatch or whose
// handler returned an error. Index is -1 when produced directly by
// Table.Dispatch outside of Run.
type DispatchError struct {
	Index int
	Op    opcodes.Op
	Cause error
}

func (e *DispatchError) Error() string {
	if e.Index < 0 {
		return fmt.Sprintf("dispatch %v: %v", e.Op, e.Cause)
	}
	return fmt.Sprintf("instruction %d (%v): %v", e.Index, e.Op, e.Cause)
}

func (e *DispatchError) Unwrap() error { return e.Cause }
