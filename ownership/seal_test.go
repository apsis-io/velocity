package ownership_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/apsis-io/velocity/ownership"
)

func TestSealRejectsNewBorrowsButKeepsExisting(t *testing.T) {
	owner := mustOwner(t, 1)
	defer owner.Release()

	held, err := owner.Borrow()
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.Seal(); err != nil {
		t.Fatal(err)
	}
	if state := owner.State(); !state.Sealed {
		t.Fatalf("state = %+v", state)
	}

	// New borrows of either kind are refused.
	if _, err := owner.Borrow(); !errors.Is(err, ownership.ErrSealed) {
		t.Fatalf("Borrow after Seal = %v", err)
	}
	if _, err := owner.BorrowMut(); !errors.Is(err, ownership.ErrSealed) {
		t.Fatalf("BorrowMut after Seal = %v", err)
	}
	if _, err := owner.View(func(int) (int, error) { return 0, nil }); !errors.Is(err, ownership.ErrSealed) {
		t.Fatalf("View after Seal = %v", err)
	}

	// The borrow taken before sealing still works.
	value, err := held.Project(func(value int) (int, error) { return value, nil })
	if err != nil || value != 1 {
		t.Fatalf("existing borrow = (%d, %v)", value, err)
	}
	if err := held.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestDrainedClosesWhenLastBorrowGoes(t *testing.T) {
	owner := mustOwner(t, 1)
	defer owner.Release()

	first, err := owner.Borrow()
	if err != nil {
		t.Fatal(err)
	}
	second, err := owner.Borrow()
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.Seal(); err != nil {
		t.Fatal(err)
	}

	drained := owner.Drained()
	select {
	case <-drained:
		t.Fatal("drained closed while borrows were outstanding")
	default:
	}

	_ = first.Release()
	select {
	case <-drained:
		t.Fatal("drained closed with one borrow still outstanding")
	default:
	}

	_ = second.Release()
	select {
	case <-drained:
	case <-time.After(time.Second):
		t.Fatal("drained did not close after the last borrow")
	}
}

// Without sealing, a borrow count of zero is transient, so the channel must not
// close: a closed channel cannot be reopened when the next borrow arrives.
func TestDrainedStaysOpenUntilSealed(t *testing.T) {
	owner := mustOwner(t, 1)
	defer owner.Release()

	drained := owner.Drained()
	select {
	case <-drained:
		t.Fatal("drained closed on an unsealed value with no borrows")
	default:
	}

	borrow, err := owner.Borrow()
	if err != nil {
		t.Fatalf("unsealed value refused a borrow: %v", err)
	}
	_ = borrow.Release()
	select {
	case <-drained:
		t.Fatal("drained closed on an unsealed value")
	default:
	}

	if err := owner.Seal(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-drained:
	case <-time.After(time.Second):
		t.Fatal("drained did not close once sealed with no borrows")
	}
}

func TestSealWithNoBorrowsDrainsImmediately(t *testing.T) {
	owner := mustOwner(t, 1)
	defer owner.Release()
	if err := owner.Seal(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-owner.Drained():
	default:
		t.Fatal("Seal with no borrows did not drain immediately")
	}
}

func TestSealIsIdempotent(t *testing.T) {
	owner := mustOwner(t, 1)
	defer owner.Release()
	for range 3 {
		if err := owner.Seal(); err != nil {
			t.Fatalf("Seal = %v", err)
		}
	}
	select {
	case <-owner.Drained():
	default:
		t.Fatal("repeated Seal did not drain")
	}
}

// The shutdown shape this exists for, with the caller owning the waiting.
func TestSealDrainReleaseShutdownSequence(t *testing.T) {
	owner := mustOwner(t, 1)
	borrow, err := owner.Borrow()
	if err != nil {
		t.Fatal(err)
	}

	// Release must still refuse while a borrow is live.
	if err := owner.Release(); !errors.Is(err, ownership.ErrConflict) {
		t.Fatalf("Release with live borrow = %v", err)
	}
	if err := owner.Seal(); err != nil {
		t.Fatal(err)
	}

	go func() {
		time.Sleep(20 * time.Millisecond)
		_ = borrow.Release()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	select {
	case <-owner.Drained():
	case <-ctx.Done():
		t.Fatal("timed out waiting to drain")
	}
	if err := owner.Release(); err != nil {
		t.Fatalf("Release after drain = %v", err)
	}
}

// A caller whose context expires first must be able to give up, leaving the
// value sealed so a later attempt can finish.
func TestSealedValueSurvivesAbandonedWait(t *testing.T) {
	owner := mustOwner(t, 1)
	borrow, err := owner.Borrow()
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.Seal(); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	select {
	case <-owner.Drained():
		t.Fatal("drained while a borrow was still held")
	case <-ctx.Done():
	}

	// Still sealed, still holding, and a later attempt completes.
	if state := owner.State(); !state.Sealed || state.Readers != 1 {
		t.Fatalf("state after abandoned wait = %+v", state)
	}
	_ = borrow.Release()
	select {
	case <-owner.Drained():
	case <-time.After(time.Second):
		t.Fatal("second attempt never drained")
	}
	if err := owner.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestSealAppliesToEveryHandle(t *testing.T) {
	owner := mustOwner(t, 1)
	shared, err := owner.IntoShared()
	if err != nil {
		t.Fatal(err)
	}
	peer, err := shared.Clone()
	if err != nil {
		t.Fatal(err)
	}

	// Sealing through one handle applies to the value, so the peer is affected.
	if err := shared.Seal(); err != nil {
		t.Fatal(err)
	}
	if _, err := peer.Borrow(); !errors.Is(err, ownership.ErrSealed) {
		t.Fatalf("peer Borrow after Seal = %v", err)
	}
	select {
	case <-peer.Drained():
	default:
		t.Fatal("peer not drained")
	}

	_ = peer.Release()
	_ = shared.Release()
}

func TestSealOnFrozenAndNilHandles(t *testing.T) {
	frozen, err := ownership.NewFrozen(1)
	if err != nil {
		t.Fatal(err)
	}
	if err := frozen.Seal(); err != nil {
		t.Fatal(err)
	}
	if _, err := frozen.Borrow(); !errors.Is(err, ownership.ErrSealed) {
		t.Fatalf("frozen Borrow after Seal = %v", err)
	}
	_ = frozen.Release()

	var nilOwner *ownership.Owner[int]
	if err := nilOwner.Seal(); !errors.Is(err, ownership.ErrReleased) {
		t.Fatalf("nil Seal = %v", err)
	}
	// Nothing to wait for, so waiting must not hang.
	select {
	case <-nilOwner.Drained():
	default:
		t.Fatal("nil Drained did not return a closed channel")
	}
}

func TestSealRejectedOnSpentHandle(t *testing.T) {
	owner := mustOwner(t, 1)
	moved, err := owner.Move()
	if err != nil {
		t.Fatal(err)
	}
	defer moved.Release()
	if err := owner.Seal(); !errors.Is(err, ownership.ErrMoved) {
		t.Fatalf("Seal on moved-from handle = %v", err)
	}
}
