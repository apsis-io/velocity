// Package opruntime is struct-frame dispatch glue: a registry mapping an
// opcodes.Op to a stateless Go handler or atom operating on an execution
// context, a read-only input frame pointer (*In), and a destination output
// frame pointer (*Out).
//
// It owns no heap-allocated register file or operand stack; all state is
// passed explicitly through (ctx, in, out) struct frames so handlers and
// compiled atoms remain allocation-free and reusable across pooled VMs.
package opruntime
