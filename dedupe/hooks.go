package dedupe

import "time"

// Hooks lets a caller observe dedup lifecycle events without Group owning any
// metrics state. Each field is called synchronously from the goroutine driving
// that event.
//
// A caller tracing spans reads them as the execution's span lifecycle: OnJoin
// — with its leader flag — is where a joining caller's span LINKS to the
// round, because an execution outlives its callers and can never be their
// span's child; OnComplete is the round's span End, carrying duration and
// outcome.
type Hooks[K comparable] struct {
	// OnJoin fires for every caller that joins a round, including its leader.
	OnJoin func(key K, leader bool)
	// OnComplete fires once per key when its callback finishes.
	OnComplete func(key K, duration time.Duration, err error)
}
