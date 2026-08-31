package ownership

import "testing"

func TestCleanupLeaseReleasesBorrow(t *testing.T) {
	owner, err := New(1)
	if err != nil {
		t.Fatal(err)
	}
	borrow, err := owner.Borrow()
	if err != nil {
		t.Fatal(err)
	}
	cleanupLease(borrow.lease)
	if state := owner.State(); state.Readers != 0 {
		t.Fatalf("state = %+v", state)
	}
	if err := borrow.Release(); err != nil {
		t.Fatal(err)
	}
	if err := owner.Release(); err != nil {
		t.Fatal(err)
	}
}
