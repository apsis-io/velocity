// Package ownership is a stub of the real package: just enough surface for
// the analyzer to resolve the acquiring methods by package path and name.
package ownership

type Owner[T any] struct{ v T }
type Shared[T any] struct{ v T }
type Frozen[T any] struct{ v T }
type ReadBorrow[T any] struct{}
type WriteBorrow[T any] struct{}
type Lease[T any] struct{}

func New[T any](v T) (*Owner[T], error)                               { return &Owner[T]{v: v}, nil }
func (o *Owner[T]) Borrow() (*ReadBorrow[T], error)                   { return nil, nil }
func (o *Owner[T]) BorrowMut() (*WriteBorrow[T], error)               { return nil, nil }
func (o *Owner[T]) View(fn func(T) (int, error)) (int, error)         { return fn(o.v) }
func (o *Owner[T]) Release() error                                    { return nil }
func (s *Shared[T]) Borrow() (*ReadBorrow[T], error)                  { return nil, nil }
func (s *Shared[T]) BorrowMut() (*WriteBorrow[T], error)              { return nil, nil }
func (f *Frozen[T]) Borrow() (*ReadBorrow[T], error)                  { return nil, nil }
func (b *ReadBorrow[T]) Release() error                               { return nil }
func (b *ReadBorrow[T]) Project(fn func(T) (int, error)) (int, error) { var z T; return fn(z) }
func (b *ReadBorrow[T]) Close() error                                 { return nil }
func (b *WriteBorrow[T]) Release() error                              { return nil }
func NewLease[T any](v T, release func(T) error) (*Lease[T], error)   { return nil, nil }
func (l *Lease[T]) Release() error                                    { return nil }
func (l *Lease[T]) Move() (*Lease[T], error)                          { return nil, nil }
