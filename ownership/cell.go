package ownership

import (
	"sync"

	"github.com/apsis-io/velocity/traits"
)

type mode uint8

const (
	modeUnique mode = iota
	modeShared
	modeReleased
)

type handleState uint8

const (
	handleActive handleState = iota
	handleMoved
	handleReleased
)

// State is a synchronized point-in-time ownership snapshot. It never contains
// or formats the owned value.
type State struct {
	Shared    bool
	Released  bool
	Moved     bool
	Readers   int
	Writer    bool
	Shares    int
	DropError error
}

type cell[T any] struct {
	mu sync.Mutex

	value T
	mode  mode

	readers int
	writer  bool
	shares  int

	drop         traits.Drop[T]
	clone        traits.Clone[T]
	dropErr      error
	dropStarted  bool
	dropFinished bool
	dropWait     chan struct{}
	nextID       uint64
}

type handle struct {
	state   handleState
	borrows int
}

func (c *cell[T]) stateFor(h *handle) State {
	if c == nil {
		return State{Released: true}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	state := State{
		Shared:    c.mode == modeShared,
		Released:  c.mode == modeReleased,
		Readers:   c.readers,
		Writer:    c.writer,
		Shares:    c.shares,
		DropError: c.dropErr,
	}
	if h != nil {
		state.Moved = h.state == handleMoved
		state.Released = state.Released || h.state == handleReleased
	}
	return state
}

func (c *cell[T]) conflict(op Operation) error {
	return &ConflictError{Operation: op, Readers: c.readers, Writer: c.writer, Shares: c.shares}
}

func (c *cell[T]) checkHandle(h *handle, op Operation) error {
	if c == nil || h == nil {
		return &ReleasedError{Operation: op}
	}
	switch h.state {
	case handleMoved:
		return &MovedError{Operation: op}
	case handleReleased:
		return &ReleasedError{Operation: op}
	}
	if c.mode == modeReleased {
		return &ReleasedError{Operation: op}
	}
	return nil
}

func (c *cell[T]) acquireRead(h *handle, expected mode) (*lease[T], error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.checkHandle(h, OpBorrow); err != nil {
		return nil, err
	}
	if c.mode != expected {
		return nil, &MovedError{Operation: OpBorrow}
	}
	if c.writer {
		return nil, c.conflict(OpBorrow)
	}
	c.readers++
	h.borrows++
	c.nextID++
	return &lease[T]{cell: c, issuer: h, id: c.nextID, kind: borrowRead}, nil
}

func (c *cell[T]) acquireWrite(h *handle, expected mode) (*lease[T], error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.checkHandle(h, OpBorrowMut); err != nil {
		return nil, err
	}
	if c.mode != expected {
		return nil, &MovedError{Operation: OpBorrowMut}
	}
	if c.writer || c.readers != 0 {
		return nil, c.conflict(OpBorrowMut)
	}
	c.writer = true
	h.borrows++
	c.nextID++
	return &lease[T]{cell: c, issuer: h, id: c.nextID, kind: borrowWrite}, nil
}

func (c *cell[T]) releaseLease(l *lease[T]) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if l.released {
		return false
	}
	l.released = true
	switch l.kind {
	case borrowRead:
		c.readers--
	case borrowWrite:
		c.writer = false
	}
	l.issuer.borrows--
	return true
}

func (c *cell[T]) project(l *lease[T], fn func(T) (any, error)) (any, error) {
	c.mu.Lock()
	if l == nil || l.released || l.kind != borrowRead {
		c.mu.Unlock()
		return nil, &ReleasedError{Operation: OpProject}
	}
	value := c.value
	c.mu.Unlock()
	return fn(value)
}

func (c *cell[T]) update(l *lease[T], fn func(*T) (any, error)) (any, error) {
	c.mu.Lock()
	if l == nil || l.released || l.kind != borrowWrite {
		c.mu.Unlock()
		return nil, &ReleasedError{Operation: OpUpdate}
	}
	value := &c.value
	c.mu.Unlock()
	return fn(value)
}
