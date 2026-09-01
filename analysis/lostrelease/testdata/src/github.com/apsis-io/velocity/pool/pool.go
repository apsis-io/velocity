// Package pool is a stub of the real package.
package pool

import "context"

type Pool[T any] struct{}
type Checkout[T any] struct{}

func (p *Pool[T]) Get(ctx context.Context) (*Checkout[T], error) { return nil, nil }
func (c *Checkout[T]) Release() error                            { return nil }
func (c *Checkout[T]) Discard() error                            { return nil }
