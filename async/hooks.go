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
	//
	// A task submitted through Submit reports index -1 and an empty label: it
	// is not a member of a collection, so there is no source index, and Submit
	// takes no name to report. It reports both waited and duration honestly,
	// which for a single task means waited is always zero — a limit bounds the
	// tasks of one operation, and a one-task operation has none to wait for.
	// Every outcome is reported, a panic and a Goexit among them, and the
	// report precedes the resolution of the handle so that awaiting a Future
	// and then reading what the hook recorded is not a race.
	OnTaskComplete func(index int, label string, waited, duration time.Duration, err error)
}
