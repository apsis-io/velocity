package ownership_test

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/apsis-io/velocity/ownership"
)

func TestLeaseHoldsThenReleasesOnce(t *testing.T) {
	var returned atomic.Int32
	lease, err := ownership.NewLease("10.0.0.7", func(ip string) error {
		if ip != "10.0.0.7" {
			t.Errorf("release got %q", ip)
		}
		returned.Add(1)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	value, err := lease.Value()
	if err != nil || value != "10.0.0.7" {
		t.Fatalf("Value = (%q, %v)", value, err)
	}
	if !lease.Held() {
		t.Fatal("lease not held")
	}

	if err := lease.Release(); err != nil {
		t.Fatalf("Release = %v", err)
	}
	for range 2 {
		if err := lease.Release(); err != nil {
			t.Fatalf("repeat Release = %v", err)
		}
	}
	if returned.Load() != 1 {
		t.Fatalf("released %d times, want 1", returned.Load())
	}
	if lease.Held() {
		t.Fatal("lease still held after release")
	}
}

// Catching use-after-release is the reason this type exists.
func TestLeaseValueAfterReleaseFails(t *testing.T) {
	lease, err := ownership.NewLease(42, func(int) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	if _, err := lease.Value(); !errors.Is(err, ownership.ErrReleased) {
		t.Fatalf("Value after release = %v", err)
	}
}

func TestLeaseReportsReleaseErrorRepeatedly(t *testing.T) {
	wantErr := errors.New("pool rejected return")
	var calls atomic.Int32
	lease, err := ownership.NewLease(1, func(int) error {
		calls.Add(1)
		return wantErr
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.Release(); !errors.Is(err, wantErr) {
		t.Fatalf("Release = %v", err)
	}
	for range 2 {
		if err := lease.Release(); !errors.Is(err, wantErr) {
			t.Fatalf("repeat Release = %v", err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("release callback ran %d times, want 1", calls.Load())
	}
}

func TestLeaseMoveSpendsOriginal(t *testing.T) {
	var returned atomic.Int32
	lease, err := ownership.NewLease(7, func(int) error { returned.Add(1); return nil })
	if err != nil {
		t.Fatal(err)
	}

	moved, err := lease.Move()
	if err != nil {
		t.Fatal(err)
	}
	if lease.Held() {
		t.Fatal("original still held after Move")
	}
	if _, err := lease.Value(); !errors.Is(err, ownership.ErrReleased) {
		t.Fatalf("Value on moved-from lease = %v", err)
	}
	// Releasing the spent handle must not hand the resource back.
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	if returned.Load() != 0 {
		t.Fatalf("moved-from handle released the resource")
	}

	value, err := moved.Value()
	if err != nil || value != 7 {
		t.Fatalf("moved Value = (%d, %v)", value, err)
	}
	if err := moved.Close(); err != nil {
		t.Fatal(err)
	}
	if returned.Load() != 1 {
		t.Fatalf("returned %d times, want 1", returned.Load())
	}
	if _, err := moved.Move(); !errors.Is(err, ownership.ErrReleased) {
		t.Fatalf("Move after release = %v", err)
	}
}

func TestLeaseRequiresRelease(t *testing.T) {
	if _, err := ownership.NewLease(1, nil); !errors.Is(err, ownership.ErrNilOption) {
		t.Fatalf("NewLease with nil release = %v", err)
	}
}

func TestLeaseNilHandle(t *testing.T) {
	var lease *ownership.Lease[int]
	if lease.Held() {
		t.Fatal("nil lease reports held")
	}
	if err := lease.Release(); err != nil {
		t.Fatalf("nil Release = %v", err)
	}
	if _, err := lease.Value(); !errors.Is(err, ownership.ErrReleased) {
		t.Fatalf("nil Value = %v", err)
	}
	if _, err := lease.Move(); !errors.Is(err, ownership.ErrReleased) {
		t.Fatalf("nil Move = %v", err)
	}
}

// Concurrent Release must hand the resource back exactly once.
func TestLeaseConcurrentReleaseIsSingular(t *testing.T) {
	var returned atomic.Int32
	lease, err := ownership.NewLease(1, func(int) error { returned.Add(1); return nil })
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 32 {
		wg.Go(func() { _ = lease.Release() })
	}
	wg.Wait()
	_ = lease.Release() // the analyzer cannot see that the loop ran
	if returned.Load() != 1 {
		t.Fatalf("returned %d times, want 1", returned.Load())
	}
}

// A Lease fits naturally into a Scope, since its release is just a func.
func TestLeaseEnrolsInScope(t *testing.T) {
	var returned atomic.Int32
	lease, err := ownership.NewLease(1, func(int) error { returned.Add(1); return nil })
	if err != nil {
		t.Fatal(err)
	}
	scope := ownership.NewScope()
	if err := scope.OnRelease(lease.Release); err != nil {
		t.Fatal(err)
	}
	if err := scope.Close(); err != nil {
		t.Fatal(err)
	}
	if returned.Load() != 1 {
		t.Fatalf("returned %d times, want 1", returned.Load())
	}
}
