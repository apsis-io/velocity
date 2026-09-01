package pool

import "time"

// Hooks lets a caller observe pool traffic without the package owning
// metrics state. Nil callbacks are skipped; each runs synchronously on the
// goroutine driving the event.
type Hooks struct {
	// OnAcquire fires for every Get. waited is time spent waiting for
	// capacity, created reports whether a resource was constructed rather
	// than reused, and err is what Get returned.
	OnAcquire func(waited time.Duration, created bool, err error)
	// OnRelease fires when a checkout ends. discarded reports whether the
	// resource was closed rather than returned to the idle set.
	OnRelease func(discarded bool, err error)
}
