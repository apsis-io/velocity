// Package ownership provides concurrency-safe runtime ownership and borrow-state
// checks for Go values.
//
// It does not provide Rust's compile-time ownership or deep immutability.
// Reference-bearing values and values returned by projections can still expose
// aliases. Callbacks, clone policies, and drop policies must return normally;
// they must not panic or call runtime.Goexit.
package ownership
