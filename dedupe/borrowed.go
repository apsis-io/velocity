package dedupe

import (
	"context"

	"github.com/apsis-io/velocity/ownership"
)

// DoBorrowed deduplicates work while the leader holds a read borrow of input.
// A follower briefly borrows input while joining, then releases it untouched.
// If ctx is canceled after work starts, the leader's loan remains held until a
// non-cooperative callback returns; OnComplete signals after that release. There
// is no batch borrowed form because partial acquisition needs application policy.
//
// The result is a plain value, as with Do; an owned group reports
// ErrOwnedResult.
func (g *Group[K, V]) DoBorrowed[I any](ctx context.Context, key K, input *ownership.Owner[I], fn func(context.Context, I) (V, error)) (V, error) {
	var zero V
	if err := g.checkBorrowed(ctx, input == nil, fn != nil); err != nil {
		return zero, err
	}
	borrow, err := input.Borrow()
	if err != nil {
		return zero, err
	}
	return g.doBorrowed(ctx, key, func() error { return borrow.Release() }, func(workCtx context.Context) (V, error) {
		return borrow.Project(func(value I) (V, error) { return fn(workCtx, value) })
	})
}

// DoBorrowedMut deduplicates work while the leader holds an exclusive mutable
// borrow of input. Two calls sharing an Owner may conflict before joining. If
// ctx is canceled after work starts, the leader's loan may remain held until a
// non-cooperative callback returns; OnComplete signals after that release.
func (g *Group[K, V]) DoBorrowedMut[I any](ctx context.Context, key K, input *ownership.Owner[I], fn func(context.Context, *I) (V, error)) (V, error) {
	var zero V
	if err := g.checkBorrowed(ctx, input == nil, fn != nil); err != nil {
		return zero, err
	}
	borrow, err := input.BorrowMut()
	if err != nil {
		return zero, err
	}
	return g.doBorrowed(ctx, key, func() error { return borrow.Release() }, func(workCtx context.Context) (V, error) {
		return borrow.Update(func(value *I) (V, error) { return fn(workCtx, value) })
	})
}

// checkBorrowed takes the nil-ness of input as a bool because a typed nil
// pointer boxed into an interface would not compare equal to nil.
func (g *Group[K, V]) checkBorrowed(ctx context.Context, nilInput, haveFn bool) error {
	switch {
	case g.owned:
		return ErrOwnedResult
	case ctx == nil:
		return ErrNilContext
	case nilInput:
		return ErrNilOwner
	case !haveFn:
		return ErrNilFunction
	}
	return ctx.Err()
}

func (g *Group[K, V]) doBorrowed(ctx context.Context, key K, release func() error, wrapped func(context.Context) (V, error)) (V, error) {
	c, leader := g.join(key)
	if !leader {
		_ = release()
		return g.wait(ctx, key, c)
	}
	go g.run(key, c, func(workCtx context.Context) (V, error) {
		defer release()
		return wrapped(workCtx)
	})
	return g.wait(ctx, key, c)
}
