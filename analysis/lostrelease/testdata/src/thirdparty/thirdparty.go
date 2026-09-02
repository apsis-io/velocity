// Package thirdparty is not velocity and is not in the analyzer's table. It
// declares its own handle and marks the function that hands one out, which
// is all a library needs to do to get the same checking.
package thirdparty

type Ticket struct{}

func (t *Ticket) Release() {}

// Draw takes a ticket that the caller must hand back.
//
//velocity:acquires
func Draw() (*Ticket, error) { return &Ticket{}, nil }

// Peek does not hand over anything, so it carries no marker.
func Peek() (*Ticket, error) { return nil, nil }
