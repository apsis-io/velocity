package ownership

import (
	"runtime"

	"github.com/apsis-io/velocity/traits"
)

// The drop net: a backstop that runs a cell's configured Drop if every handle
// to the cell becomes unreachable without an explicit release. It exists
// because the cost of a lost `defer owner.Close()` is a resource held until
// process end — a file descriptor per container, in the report that asked for
// this — while every other failure of the discipline surfaces as an error.
//
// It is a backstop and not a second lifetime. The runtime gives no guarantee
// that a cleanup runs before the program exits, so nothing may depend on the
// net: a resource that must be closed on the way out is closed explicitly, and
// the net covers only the drop that was forgotten. Drop runs on a goroutine of
// the runtime's choosing, so a Drop must not assume the caller's goroutine.

// backstop is what the net's cleanup runs against. It is the cleanup's arg and
// the cell is the ptr, so **backstop must never reference the cell** — an arg
// that keeps the ptr reachable means the cell is never collected and the net
// never fires, which AddCleanup cannot fully guard against. In consequence a
// Drop given to WithDrop must not capture the Owner it is handed to; a closure
// over the resource itself is the shape that works, which is what NewCloser
// builds and what the value and drop fields here hold directly.
//
// The value field tracks the cell's current value: the net drops what the cell
// owns now, not what it owned at construction, so a Mutate that replaces the
// value is followed by a sync under the cell lock. Once the cell is
// unreachable no method can run to move the field again, so the cleanup reads
// the final value without a race.
type backstop[T any] struct {
	value    T
	drop     traits.Drop[T]
	disarmed bool
}

func (b *backstop[T]) run() {
	if b.disarmed || b.drop == nil {
		return
	}

	// The error has nowhere to go: whatever could have read it is
	// unreachable. A dropped Drop's failure is exactly the kind of thing the
	// debug build's log is for, which is all this leaves behind.
	_ = b.drop(b.value)

	logNetDrop()
}

// armNet registers the drop net on a cell. Cells built without a Drop get no
// net — there is nothing for one to run — and arming is what every
// drop-bearing construction calls instead of repeating that decision.
//
// Call it once, at construction, with no cell lock held.
func armNet[T any](c *cell[T]) {
	if c.drop == nil {
		return
	}

	b := &backstop[T]{value: c.value, drop: c.drop}
	c.net = runtime.AddCleanup(c, (*backstop[T]).run, b)
	c.netArg = b
}

// disarmNetLocked takes the net down. Called on every path where the cell
// stops being the drop's owner — explicit release, Detach, Map — inside the
// critical section that performs the transition.
//
// The stop is always effective here: a cleanup is queued only once the cell is
// unreachable, and a method on a live handle proves the cell reachable, so
// Stop cannot lose to a queued cleanup on any current path. The flag is the
// belt to that brace, so a future path that gets the reachability wrong fails
// quiet rather than dropping twice.
func (c *cell[T]) disarmNetLocked() {
	if c.netArg == nil {
		return
	}

	c.netArg.disarmed = true
	c.net.Stop()
}

// syncNetLocked records the value the net would drop. Called after anything
// that wrote c.value in place — the scoped and queued mutations — with the
// lock that ordered the write.
func (c *cell[T]) syncNetLocked() {
	if c.netArg != nil {
		c.netArg.value = c.value
	}
}
