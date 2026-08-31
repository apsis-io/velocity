package ownership

import (
	"errors"
	"io"
	"slices"
	"sync"
)

// Scope collects heterogeneous resources acquired during a multi-step
// construction and releases them together if that construction does not
// finish. It replaces the manual cascade a constructor otherwise grows, where
// each new acquisition has to close everything opened before it:
//
//	scope := ownership.NewScope()
//	defer scope.Close()
//
//	conn, err := dial()
//	if err != nil {
//	    return nil, err
//	}
//	scope.OwnCloser(conn)
//
//	raw, err := dial()
//	if err != nil {
//	    return nil, err  // scope.Close closes conn
//	}
//	scope.OwnCloser(raw)
//
//	scope.Disarm()       // the bundle below now owns everything
//	return &Bundle{conn: conn, raw: raw}, nil
//
// Release runs in reverse acquisition order and continues past failures,
// joining every error. Cleanup is explicit only: a Scope that is never closed
// releases nothing, because a dropped Scope is indistinguishable from one whose
// resources were deliberately transferred.
//
// A Scope is safe for concurrent use, though the construction it guards is
// usually sequential.
type Scope struct {
	mu       sync.Mutex
	releases []func() error
	closed   bool
	disarmed bool
}

// NewScope creates an empty Scope.
func NewScope() *Scope { return &Scope{} }

// Own transfers an Owner into the scope, which then releases it. The Owner is
// moved, so the caller's handle is spent and further use reports ErrMoved; the
// value stays reachable only through the scope. Its configured Drop still runs
// on release.
//
// Returns the move error, and does not enrol the owner, if it cannot be taken:
// an outstanding borrow, or an already spent handle.
func (s *Scope) Own[T any](owner *Owner[T]) error {
	if owner == nil {
		return &ReleasedError{Operation: OpOwn}
	}
	moved, err := owner.Move()
	if err != nil {
		return err
	}
	return s.OnRelease(moved.Release)
}

// OwnCloser transfers an io.Closer into the scope, which closes it on release.
// It is Own without first constructing an Owner.
func (s *Scope) OwnCloser(closer io.Closer) error {
	if closer == nil {
		return &ReleasedError{Operation: OpOwn}
	}
	return s.OnRelease(closer.Close)
}

// OnRelease registers an arbitrary cleanup, for resources that are neither an
// Owner nor an io.Closer: a permit to hand back, an allocation to return, a
// registration to undo.
//
// It reports ErrScopeClosed once the scope has been closed or disarmed, so a
// resource acquired after that point is never silently forgotten. The caller
// still owns it and must release it directly.
func (s *Scope) OnRelease(release func() error) error {
	if release == nil {
		return &ReleasedError{Operation: OpOwn}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.disarmed {
		return &ScopeError{Operation: OpOwn, Cause: ErrScopeClosed}
	}
	s.releases = append(s.releases, release)
	return nil
}

// Disarm gives up responsibility for everything enrolled, for the success path
// where a constructed value has taken over. Close then does nothing, so the
// usual `defer scope.Close()` stays correct.
//
// It returns the number of resources released, which is a convenient way to
// assert in tests that a scope actually held what was expected.
func (s *Scope) Disarm() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := len(s.releases)
	s.disarmed = true
	s.releases = nil
	return count
}

// Close releases everything enrolled, in reverse order of acquisition, and
// continues past failures so one stubborn resource cannot strand the rest. It
// returns every error joined.
//
// It is idempotent, and does nothing after Disarm.
func (s *Scope) Close() error {
	s.mu.Lock()
	if s.closed || s.disarmed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	releases := s.releases
	s.releases = nil
	s.mu.Unlock()

	// Reverse order: later resources are the ones likely built on earlier ones.
	var errs []error
	for _, release := range slices.Backward(releases) {
		if err := release(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Len reports how many resources the scope would release.
func (s *Scope) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.releases)
}
