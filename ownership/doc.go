// Package ownership provides concurrency-safe runtime ownership and borrow-state
// checks for Go values.
//
// A value with a configured Drop is backed by the drop net: if every handle to
// it becomes unreachable without a release having run, the net runs the Drop.
// The net is a backstop for a lost release, not a second lifetime — the
// runtime does not guarantee that a cleanup runs before the program exits, so
// nothing may be put off to it that must happen on the way out. See
// NewCloser's doc for the shape and the obligations it adds.
//
// It does not provide Rust's compile-time ownership or deep immutability.
// Reference-bearing values and values returned by projections can still expose
// aliases. Callbacks, clone policies, and drop policies must return normally;
// they must not panic or call runtime.Goexit.
package ownership
