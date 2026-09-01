// Package async is a stub of the real package.
package async

import "context"

type Semaphore struct{}
type Mutex struct{}
type Permit struct{}

func NewMutex() *Mutex                                            { return &Mutex{} }
func (s *Semaphore) Acquire(ctx context.Context) (*Permit, error) { return nil, nil }
func (s *Semaphore) TryAcquire() (*Permit, bool)                  { return nil, false }
func (m *Mutex) Lock(ctx context.Context) (*Permit, error)        { return nil, nil }
func (m *Mutex) TryLock() (*Permit, bool)                         { return nil, false }
func (p *Permit) Release()                                        {}
