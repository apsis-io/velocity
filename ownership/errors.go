package ownership

import (
	"errors"
	"fmt"
)

var (
	ErrConflict        = errors.New("ownership conflict")
	ErrMoved           = errors.New("ownership moved")
	ErrReleased        = errors.New("ownership released")
	ErrNoClone         = errors.New("clone trait is not configured")
	ErrInvalidConfig   = errors.New("invalid ownership configuration")
	ErrProjection      = errors.New("invalid ownership projection")
	ErrDuplicateOption = errors.New("duplicate ownership option")
	ErrNilOption       = errors.New("nil ownership option")
)

type Operation string

const (
	OpBorrow     Operation = "borrow"
	OpBorrowMut  Operation = "borrow mutable"
	OpMove       Operation = "move"
	OpIntoValue  Operation = "into value"
	OpIntoShared Operation = "into shared"
	OpClone      Operation = "clone shared"
	OpIntoOwner  Operation = "into owner"
	OpRelease    Operation = "release"
	OpProject    Operation = "project"
	OpUpdate     Operation = "update"
	OpSnapshot   Operation = "snapshot"
)

// ConflictError describes the state that rejected an operation.
type ConflictError struct {
	Operation Operation
	Readers   int
	Writer    bool
	Shares    int
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf("%s: %v (readers=%d writer=%t shares=%d)", e.Operation, ErrConflict, e.Readers, e.Writer, e.Shares)
}

func (e *ConflictError) Unwrap() error { return ErrConflict }

// MovedError identifies an operation on a consumed handle.
type MovedError struct{ Operation Operation }

func (e *MovedError) Error() string { return fmt.Sprintf("%s: %v", e.Operation, ErrMoved) }
func (e *MovedError) Unwrap() error { return ErrMoved }

// ReleasedError identifies an operation on a released handle or cell.
type ReleasedError struct{ Operation Operation }

func (e *ReleasedError) Error() string { return fmt.Sprintf("%s: %v", e.Operation, ErrReleased) }
func (e *ReleasedError) Unwrap() error { return ErrReleased }

// NoCloneError identifies a snapshot without a configured clone trait.
type NoCloneError struct{ Operation Operation }

func (e *NoCloneError) Error() string { return fmt.Sprintf("%s: %v", e.Operation, ErrNoClone) }
func (e *NoCloneError) Unwrap() error { return ErrNoClone }

// ConfigError describes an invalid constructor option.
type ConfigError struct {
	Option string
	Reason error
}

func (e *ConfigError) Error() string {
	return fmt.Sprintf("ownership option %q: %v", e.Option, e.Reason)
}
func (e *ConfigError) Unwrap() []error { return []error{ErrInvalidConfig, e.Reason} }

// ProjectionError identifies an invalid projection or update callback.
type ProjectionError struct{ Operation Operation }

func (e *ProjectionError) Error() string { return fmt.Sprintf("%s: %v", e.Operation, ErrProjection) }
func (e *ProjectionError) Unwrap() error { return ErrProjection }
