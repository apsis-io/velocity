package async

import "time"

// Hooks lets callers observe task timing without the package owning metrics
// state. Each callback runs in the task goroutine; nil callbacks are skipped.
type Hooks struct {
	// OnTaskComplete runs once for every task. waited is time spent waiting for
	// a concurrency permit, while duration is time spent in Task.Run. Both are
	// zero when Run was never started because the context was canceled while
	// waiting for a permit.
	OnTaskComplete func(index int, label string, waited, duration time.Duration, err error)
}
