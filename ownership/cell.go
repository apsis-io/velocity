package ownership

import (
	"sync"

	"github.com/apsis-io/velocity/traits"
)

type mode uint8

const (
	modeUnique mode = iota
	modeShared
	modeFrozen
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
	Frozen    bool
	Sealed    bool
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

	// sealed rejects new borrows so an in-progress retirement cannot be
	// extended. drained closes once sealing has taken effect and the last
	// borrow has gone, which is the point it is safe to release.
	sealed        bool
	drained       chan struct{}
	drainedClosed bool

	drop    traits.Drop[T]
	clone   traits.Clone[T]
	dropErr error
	nextID  uint64
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
		Frozen:    c.mode == modeFrozen,
		Sealed:    c.sealed,
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
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conflictLocked(op)
}

func (c *cell[T]) conflictLocked(op Operation) error {
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
	if c.sealed {
		return nil, &SealedError{Operation: OpBorrow}
	}
	if c.writer {
		return nil, c.conflictLocked(OpBorrow)
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
	if c.sealed {
		return nil, &SealedError{Operation: OpBorrowMut}
	}
	if c.writer || c.readers != 0 {
		return nil, c.conflictLocked(OpBorrowMut)
	}
	c.writer = true
	h.borrows++
	c.nextID++
	return &lease[T]{cell: c, issuer: h, id: c.nextID, kind: borrowWrite}, nil
}

func (c *cell[T]) releaseLease(l *lease[T]) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.releaseLeaseLocked(l)
}

// releaseLeaseLocked assumes c.mu is held. Map needs to release its lease and
// commit the transfer in one critical section, so that no borrow can be
// acquired in between.
func (c *cell[T]) releaseLeaseLocked(l *lease[T]) bool {
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
	c.signalDrainedLocked()
	return true
}

// drainedChanLocked returns the drain channel, creating it on first use.
func (c *cell[T]) drainedChanLocked() chan struct{} {
	if c.drained == nil {
		c.drained = make(chan struct{})
	}
	return c.drained
}

// signalDrainedLocked closes the drain channel once sealing is in effect and
// no borrow remains. Sealing is irreversible and rejects new borrows, so that
// condition is terminal rather than transient, which is what makes closing a
// channel the right signal for it.
func (c *cell[T]) signalDrainedLocked() {
	if c.drainedClosed || !c.sealed || c.readers != 0 || c.writer {
		return
	}
	c.drainedClosed = true
	close(c.drainedChanLocked())
}

// seal rejects further borrows and reports whether the cell is already drained.
func (c *cell[T]) seal(h *handle) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.checkHandle(h, OpSeal); err != nil {
		return err
	}
	c.sealed = true
	c.signalDrainedLocked()
	return nil
}

func (c *cell[T]) drainedChan() <-chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.drainedChanLocked()
}
