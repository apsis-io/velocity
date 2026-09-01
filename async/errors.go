package async

import (
	"errors"
	"fmt"
)

var (
	ErrInvalidPlan  = errors.New("invalid async plan")
	ErrInvalidLimit = errors.New("invalid async limit")
	ErrNilTask      = errors.New("nil async task")
	ErrNilOwner     = errors.New("nil async owner")
	ErrNoTasks      = errors.New("async plan has no tasks")
	ErrNilContext   = errors.New("nil async context")
	ErrNilPipeline  = errors.New("nil async pipeline stage")
	ErrClosed       = errors.New("async group closed")
)

// PlanError identifies plan configuration that cannot execute.
type PlanError struct {
	Index int
	Cause error
}

func (e *PlanError) Error() string {
	if e.Index < 0 {
		return fmt.Sprintf("async plan: %v", e.Cause)
	}
	return fmt.Sprintf("async plan: task %d: %v", e.Index, e.Cause)
}

func (e *PlanError) Unwrap() []error { return []error{ErrInvalidPlan, e.Cause} }

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
