package traits

import (
	"errors"
	"fmt"
)

// ErrCallbackExit reports a callback that ended without returning — a
// runtime.Goexit, which runs the deferred release and then ends the goroutine
// without recovering and without returning.
//
// Without it a callback that never returned is indistinguishable from one that
// returned the zero value, so the work is reported as a success it did not
// have. dedupe had this exact problem and named it there; one concept belongs
// beside Panic rather than in each package that can hit it.
var ErrCallbackExit = errors.New("callback exited without returning")

// Panic captures a callback panic together with the stack it came from, so a
// panic that cannot reach its caller — because the caller has already returned
// — is still reported rather than lost or allowed to take the process with it.
//
// It was defined twice, identically, in async and in ownership. One concept
// with two types means a caller recovering a panic from either has to handle
// both, and errors.As against one misses the other.
type Panic struct {
	Value any
	Stack []byte
}

func (p *Panic) Error() string { return fmt.Sprintf("panic: %v\n%s", p.Value, p.Stack) }

func (p *Panic) Unwrap() error {
	if err, ok := p.Value.(error); ok {
		return err
	}

	return nil
}
