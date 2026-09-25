package async

import (
	"errors"
	"fmt"
)

var (
	ErrInvalidTask  = errors.New("invalid async task")
	ErrInvalidLimit = errors.New("invalid async limit")
	ErrNilTask      = errors.New("nil async task")
	ErrNilOwner     = errors.New("nil async owner")
	ErrNoTasks      = errors.New("async has no tasks")
	ErrNilContext   = errors.New("nil async context")
	ErrNilPipeline  = errors.New("nil async pipeline stage")
	ErrClosed       = errors.New("async group closed")
	ErrNilReceiver  = errors.New("nil async receiver")
	ErrNilOption    = errors.New("nil async option")
)

// TaskError identifies a runner or a task that cannot execute. It is
// wrapped in ErrInvalidTask, which is a name the package kept after Plan was
// removed; the type reports the same condition and no longer names a type
// that cannot be constructed.
type TaskError struct {
	Index int
	Cause error
}

func (e *TaskError) Error() string {
	if e.Index < 0 {
		return fmt.Sprintf("async task: %v", e.Cause)
	}

	return fmt.Sprintf("async task: task %d: %v", e.Index, e.Cause)
}

func (e *TaskError) Unwrap() []error { return []error{ErrInvalidTask, e.Cause} }

// PipelineError identifies an invalid pipeline stage.
type PipelineError struct {
	Cause error
}

func (e *PipelineError) Error() string { return fmt.Sprintf("async pipeline: %v", e.Cause) }
func (e *PipelineError) Unwrap() error { return e.Cause }

// ItemError is one failed item of a Map or ForEach, joined with its siblings
// into the returned error. errors.Is and errors.As see through both the join
// and the item to the underlying cause.
type ItemError struct {
	Index int
	Err   error
}

func (e *ItemError) Error() string { return fmt.Sprintf("item %d: %v", e.Index, e.Err) }
func (e *ItemError) Unwrap() error { return e.Err }

// Failures extracts every *ItemError from a Map or ForEach error, in index
// order, or nil if err carries none. At a boundary that wants one message —
// a status field, an event — Failures(err)[0] is the lowest failed item.
func Failures(err error) []*ItemError {
	var (
		items []*ItemError
		walk  func(error)
	)

	walk = func(err error) {
		switch e := err.(type) {
		case nil:
		case *ItemError:
			items = append(items, e)
		case interface{ Unwrap() []error }:
			for _, inner := range e.Unwrap() {
				walk(inner)
			}
		case interface{ Unwrap() error }:
			walk(e.Unwrap())
		}
	}
	walk(err)

	return items
}
