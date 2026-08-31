// Package opruntime is glue: a registry mapping an opcodes.Op to the Go
// function that implements it, plus dispatch.
//
// It is not a virtual machine. It owns no registers, no operand stack, and
// no execution state; all state lives in closures the caller supplies as
// Handlers.
package opruntime
