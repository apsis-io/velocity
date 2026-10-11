package nsync

// noCopy is embedded in types whose doc comments forbid copying after
// first use, so that go vet's copylocks analysis enforces mechanically
// what the documentation states. It implements sync.Locker, which is what
// the analyzer looks for, and does nothing: the zero value is valid, and
// Lock and Unlock are no-ops. TryMutex deliberately does not embed it —
// value copies of a TryMutex share its lock, which is that type's
// documented feature.
type noCopy struct{}

func (*noCopy) Lock()   {}
func (*noCopy) Unlock() {}
