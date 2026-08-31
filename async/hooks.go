package async

import "time"

// Hooks lets callers observe task timing without the package owning metrics
// state. Nil callbacks are skipped.
type Hooks struct {
	// OnTaskComplete runs once for every task. waited is time spent waiting for
	// a concurrency permit, while duration is time spent in Task.Run.
	//
	// It runs in the task's own goroutine, except for tasks that never start
	// because the context was canceled first, which have no goroutine and are
	// reported from the caller's. Those report duration zero, and waited only
	// for the one task that was actually queued for a permit.
	OnTaskComplete func(index int, label string, waited, duration time.Duration, err error)
}
