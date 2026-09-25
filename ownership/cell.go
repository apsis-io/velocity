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

	// pending counts submitted mutations that have been handed a Future but not
	// yet admitted — work the caller has committed and that no borrow accounts
	// for. Release and Move refuse while it is non-zero, so ending a handle is
	// the caller's decision rather than a race with a goroutine this cell has
	// already promised to run.
	pending int

	// changed is closed and replaced whenever admission-relevant state moves: a
	// borrow ends, or the cell is sealed. It is a **broadcast, not a queue** —
	// closing it hands every waiter a chance and holds nothing, so a waiter can
	// never block a release and this package still contains no blocking
	// operation. A waiter that loses the race simply re-reads the state.
	//
	// Deliberately unordered. An ordered waiter list would be a queue the cell
	// owns, and a re-entrant caller would silently queue behind itself with
	// nothing timing it out, which is the deadlock this broadcast exists to
	// avoid. Fairness is a question for a measurement, not a default.
	changed chan struct{}

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

// admitReadLocked applies every precondition for a read borrow and, if they
// hold, counts it. Scoped and advanced borrows share it, so they cannot
// disagree about what a read is allowed to coexist with.
func (c *cell[T]) admitReadLocked(h *handle, expected mode) error {
	if err := c.checkHandle(h, OpBorrow); err != nil {
		return err
	}

	if c.mode != expected {
		return &MovedError{Operation: OpBorrow}
	}

	if c.sealed {
		return &SealedError{Operation: OpBorrow}
	}

	if c.writer {
		return c.conflictLocked(OpBorrow)
	}

	c.readers++
	h.borrows++

	return nil
}

// admitWriteLocked is admitReadLocked for an exclusive borrow.
func (c *cell[T]) admitWriteLocked(h *handle, expected mode) error {
	if err := c.checkHandle(h, OpBorrowMut); err != nil {
		return err
	}

	if c.mode != expected {
		return &MovedError{Operation: OpBorrowMut}
	}

	if c.sealed {
		return &SealedError{Operation: OpBorrowMut}
	}

	if c.writer || c.readers != 0 {
		return c.conflictLocked(OpBorrowMut)
	}

	c.writer = true
	h.borrows++

	return nil
}

// endReadLocked undoes admitReadLocked; endWriteLocked, admitWriteLocked.
func (c *cell[T]) endReadLocked(h *handle) {
	c.readers--
	h.borrows--

	c.changedLocked()
	c.signalDrainedLocked()
}

func (c *cell[T]) endWriteLocked(h *handle) {
	c.writer = false
	h.borrows--

	c.changedLocked()
	c.signalDrainedLocked()
}

// changedLocked wakes every goroutine waiting for admission to re-read the
// cell's state. Closing a channel is not a wait, so this adds a signal without
// adding a blocking operation.
// settlePending drops one submission's count. Called on every path a submitted
// mutation can finish, because a count that only falls on the happy path
// blocks Release and Move for good — and the paths that are easiest to forget
// are exactly the ones a release or a seal causes.
func (c *cell[T]) settlePending() {
	c.mu.Lock()
	c.pending--
	c.mu.Unlock()
}

func (c *cell[T]) changedLocked() {
	if c.changed != nil {
		close(c.changed)
	}

	c.changed = make(chan struct{})
}

// waitForChange returns the channel to select on until the cell's state next
// moves, or ctx ends. Called with the lock held; the caller releases it before
// selecting, which is what keeps a waiter from blocking a release.
func (c *cell[T]) waitForChange() <-chan struct{} {
	if c.changed == nil {
		c.changed = make(chan struct{})
	}

	return c.changed
}

func (c *cell[T]) acquireRead(h *handle, expected mode) (*lease[T], error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.admitReadLocked(h, expected); err != nil {
		return nil, err
	}

	c.nextID++

	return &lease[T]{cell: c, issuer: h, id: c.nextID, kind: borrowRead}, nil
}

func (c *cell[T]) acquireWrite(h *handle, expected mode) (*lease[T], error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.admitWriteLocked(h, expected); err != nil {
		return nil, err
	}

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
		c.endReadLocked(l.issuer)
	case borrowWrite:
		c.endWriteLocked(l.issuer)
	}

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
	c.changedLocked()
	c.signalDrainedLocked()

	return nil
}

func (c *cell[T]) drainedChan() <-chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.drainedChanLocked()
}
