package a

import (
	"context"
	"errors"

	"github.com/apsis-io/velocity/ownership"
	"github.com/apsis-io/velocity/pool"
)

var cond bool

func deferred(owner *ownership.Owner[int]) error {
	borrow, err := owner.Borrow()
	if err != nil {
		return err
	}
	defer borrow.Release()
	return nil
}

func explicit(owner *ownership.Owner[int]) error {
	borrow, err := owner.Borrow()
	if err != nil {
		return err
	}
	if cond {
		return borrow.Close()
	}
	return borrow.Release()
}

func errNilFirst(owner *ownership.Owner[int]) error {
	borrow, err := owner.Borrow()
	if err == nil {
		defer borrow.Release()
	}
	return err
}

func discarded(owner *ownership.Owner[int]) {
	_, _ = owner.Borrow() // want "the handle returned by ownership.Owner.Borrow should be released, not discarded"
}

func probed(owner *ownership.Owner[int]) bool {
	// A blank handle whose error is inspected in the same if is a probe of
	// whether acquisition fails, which tests use to assert conflicts.
	if _, err := owner.Borrow(); err != nil {
		return false
	}
	_, err := owner.Borrow() // want "the handle returned by ownership.Owner.Borrow should be released, not discarded"
	return err == nil
}

func leakedOnBranch(owner *ownership.Owner[int]) error {
	borrow, err := owner.BorrowMut() // want "borrow returned by ownership.Owner.BorrowMut is not released on all paths"
	if err != nil {
		return err
	}
	if cond {
		return errors.New("early") // want "this return statement may be reached without releasing borrow acquired on line [0-9]+"
	}
	return borrow.Release()
}

func projectedThenForgotten(owner *ownership.Owner[int]) (int, error) {
	borrow, err := owner.Borrow() // want "borrow returned by ownership.Owner.Borrow is not released on all paths"
	if err != nil {
		return 0, err
	}
	// Project uses the resource without discharging it.
	return borrow.Project(func(v int) (int, error) { return v, nil }) // want "this return statement may be reached without releasing borrow acquired on line [0-9]+"
}

func blankAssignIsNotAUse(owner *ownership.Owner[int]) {
	borrow, _ := owner.Borrow() // want "borrow returned by ownership.Owner.Borrow is not released on all paths"
	_ = borrow
} // want "this return statement may be reached without releasing borrow acquired on line [0-9]+"

func passedIsAUse(owner *ownership.Owner[int], take func(*ownership.ReadBorrow[int])) {
	borrow, _ := owner.Borrow()
	take(borrow) // responsibility may have moved with it
}

func handedOff(owner *ownership.Owner[int], sink chan *ownership.ReadBorrow[int]) error {
	borrow, err := owner.Borrow()
	if err != nil {
		return err
	}
	sink <- borrow // a use: responsibility moved elsewhere
	return nil
}

func returned(owner *ownership.Owner[int]) (*ownership.ReadBorrow[int], error) {
	return owner.Borrow() // not bound to a variable here: nothing to check
}

func shared(s *ownership.Shared[int], f *ownership.Frozen[int]) {
	a, _ := s.BorrowMut() // want "a returned by ownership.Shared.BorrowMut is not released on all paths"
	b, _ := f.Borrow()
	_ = b.Release()
	if cond {
		return // want "this return statement may be reached without releasing a acquired on line [0-9]+"
	}
	_ = a.Release()
}

func lease(ip string, give func(string) error) error {
	l, err := ownership.NewLease(ip, give) // want "l returned by ownership.NewLease is not released on all paths"
	if err != nil {
		return err
	}
	if cond {
		return nil // want "this return statement may be reached without releasing l acquired on line [0-9]+"
	}
	return l.Release()
}

func checkout(ctx context.Context, p *pool.Pool[int]) error {
	c, err := p.Get(ctx)
	if err != nil {
		return err
	}
	if cond {
		return c.Discard()
	}
	defer c.Release()
	return nil
}

func inClosure(owner *ownership.Owner[int]) func() error {
	return func() error {
		borrow, err := owner.Borrow() // want "borrow returned by ownership.Owner.Borrow is not released on all paths"
		if err != nil {
			return err
		}
		if cond {
			return nil // want "this return statement may be reached without releasing borrow acquired on line [0-9]+"
		}
		return borrow.Release()
	}
}

func viewIsNotABorrow(owner *ownership.Owner[int]) (int, error) {
	return owner.View(func(v int) (int, error) { return v, nil })
}
