// Package fixture is compiled against the real velocity packages, not the
// analyzer's stubs, so the end-to-end test proves the analyzer still
// recognises the real acquiring methods after any rename.
package fixture

import (
	"context"

	"github.com/apsis-io/velocity/ownership"
	"github.com/apsis-io/velocity/pool"
)

var cond bool

// Clean: released on every path.
func Clean(owner *ownership.Owner[int]) (int, error) {
	borrow, err := owner.Borrow()
	if err != nil {
		return 0, err
	}
	defer borrow.Release()
	return borrow.Project(func(v int) (int, error) { return v, nil })
}

// LeakedBorrow returns early without releasing.
func LeakedBorrow(owner *ownership.Owner[int]) error {
	borrow, err := owner.BorrowMut()
	if err != nil {
		return err
	}
	if cond {
		return nil // leak
	}
	return borrow.Release()
}

// LeakedCheckout projects the pooled value and forgets the checkout.
func LeakedCheckout(ctx context.Context, p *pool.Pool[int]) (int, error) {
	c, err := p.Get(ctx)
	if err != nil {
		return 0, err
	}
	return c.Value()
}

// DiscardedLease throws the lease away.
func DiscardedLease(give func(string) error) {
	_, _ = ownership.NewLease("10.0.0.1", give)
}
