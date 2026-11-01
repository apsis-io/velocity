package async

import (
	"context"
	"sync/atomic"

	"github.com/apsis-io/nexty"
	"github.com/apsis-io/velocity/traits"
)

// Once performs an action exactly once. Its zero value is ready to use, and it
// must not be copied after first use.
type Once = nexty.Once

// OnceFunc returns a function that invokes f only once.
func OnceFunc(f func()) func() {
	return nexty.OnceFunc(f)
}

// OnceValue returns a function that invokes f only once and returns the value
// returned by f.
func OnceValue[T any](f func() T) func() T {
	return nexty.OnceValue(f)
}

// OnceValues returns a function that invokes f only once and returns the values
// returned by f.
func OnceValues[T1, T2 any](f func() (T1, T2)) func() (T1, T2) {
	return nexty.OnceValues(f)
}

// OnceFuture computes a Future[T] lazily on first request. The first caller to
// Resolve or Do runs fn and resolves the shared Future; all concurrent and
// subsequent callers receive the same Future without blocking if they use
// Future.Try or Future.OnComplete, or wait under their own context via Await.
type OnceFuture[T any] struct {
	state atomic.Int32
	fut   atomic.Pointer[traits.Future[T]]
}

// Resolve starts fn on the first call and returns the shared Future[T].
// If fn has already been started or completed, Resolve returns the existing
// Future[T] immediately without running fn.
func (o *OnceFuture[T]) Resolve(fn func() (T, error)) *traits.Future[T] {
	if f := o.fut.Load(); f != nil {
		return f
	}

	candidate := traits.NewFuture[T]()
	if o.fut.CompareAndSwap(nil, candidate) {
		val, err := fn()
		candidate.Complete(val, err)

		return candidate
	}

	return o.fut.Load()
}

// Await resolves the OnceFuture using fn if needed and awaits the outcome under ctx.
func (o *OnceFuture[T]) Await(ctx context.Context, fn func() (T, error)) (T, error) {
	return o.Resolve(fn).Await(ctx)
}
