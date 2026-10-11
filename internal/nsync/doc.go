// Package nsync is velocity's private copy of the lock core from
// github.com/apsis-io/nsync — a fork of github.com/vburenin/nsync (MIT,
// copyright Volodymyr Burenin; see LICENSE) carrying the rewritten lock
// core and the zero-value-valid inline Mutex.
//
// It is dissolved into velocity rather than imported because velocity is
// the fork's only consumer: the types here — TryMutex, Mutex, Semaphore,
// and the lockState core — back the ownership cell, the pool's permit and
// lock, and the async primitives, and no velocity API exposes them. The
// fork's repository keeps the history and the types this copy does not
// carry (named locks, flags, the controlled wait group).
//
// Lock semantics are the fork's: a barging single-word fast path, user
// space queues with waiter reuse, spin before park, and handoff to a
// starving queue head after a bounded wait.
package nsync
