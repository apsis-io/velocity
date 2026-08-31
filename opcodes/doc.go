// Package opcodes defines plain data shapes for identifying an operation and
// its operands.
//
// It performs no encoding and no execution and defines no domain-specific
// operations. Domain semantics belong to packages built on top: opruntime
// dispatches an Op to a Go function, and a domain package (such as a future
// ownership bytecode adapter) declares what each Op means.
package opcodes
