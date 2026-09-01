// Package pool holds a bounded set of reusable resources — connections,
// buffers, handles — and checks them out as ownership leases, so returning
// one exactly once and never using it afterwards is enforced rather than
// hoped for.
package pool
