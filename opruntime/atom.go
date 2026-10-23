package opruntime

import "unsafe"

// AtomFn is a typed struct-frame operation taking an execution context, a
// pointer to the input frame struct, and a pointer to the destination output
// frame struct — the Go generic counterpart to:
//
//	typedef void (*MalfAtomFn)(void* ctx, const void* in, void* out);
type AtomFn[Ctx, In, Out any] func(ctx Ctx, in *In, out *Out) error

// RawAtomFn is a type-erased struct-frame atom whose input and output frame
// pointers are passed as unsafe.Pointer so heterogeneous frame atoms can be
// stored in a single slice or dispatch table without boxing or reflection.
type RawAtomFn[Ctx any] func(ctx Ctx, in, out unsafe.Pointer) error

// BindAtom erases a typed AtomFn[Ctx, In, Out] into a RawAtomFn[Ctx] using
// zero-copy pointer casts.
func BindAtom[Ctx, In, Out any](fn AtomFn[Ctx, In, Out]) RawAtomFn[Ctx] {
	if fn == nil {
		return nil
	}

	return func(ctx Ctx, in, out unsafe.Pointer) error {
		return fn(ctx, (*In)(in), (*Out)(out))
	}
}

// CallAtom invokes a type-erased RawAtomFn[Ctx] with typed *In and *Out
// struct-frame pointers.
func CallAtom[Ctx, In, Out any](atom RawAtomFn[Ctx], ctx Ctx, in *In, out *Out) error {
	if atom == nil {
		return ErrNilHandler
	}

	return atom(ctx, unsafe.Pointer(in), unsafe.Pointer(out))
}
