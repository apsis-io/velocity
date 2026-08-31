package dedupe

import "time"

// Hooks lets a caller observe dedup lifecycle events without Group owning any
// metrics state. Each field is called synchronously from the goroutine driving
// that event.
type Hooks[K comparable] struct {
	// OnJoin fires for every caller that joins a round, including its leader.
	OnJoin func(key K, leader bool)
	// OnComplete fires once per key when its callback finishes.
	OnComplete func(key K, duration time.Duration, err error)
}
