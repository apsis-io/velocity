package ownership_test

import (
	"sync"
	"testing"

	"github.com/apsis-io/velocity/ownership"
)

var ownershipSink int

func BenchmarkOwner(b *testing.B) {
	b.Run("new-release", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			owner, _ := ownership.New(1)
			_ = owner.Release()
		}
	})
	b.Run("state", func(b *testing.B) {
		owner, _ := ownership.New(1)
		defer owner.Release()
		b.ReportAllocs()
		for b.Loop() {
			_ = owner.State()
		}
	})
	b.Run("scoped-read", func(b *testing.B) {
		owner, _ := ownership.New(1)
		defer owner.Release()
		b.ReportAllocs()
		for b.Loop() {
			ownershipSink, _ = owner.View(func(value int) (int, error) { return value, nil })
		}
	})
	b.Run("advanced-read", func(b *testing.B) {
		owner, _ := ownership.New(1)
		defer owner.Release()
		b.ReportAllocs()
		for b.Loop() {
			borrow, _ := owner.Borrow()
			ownershipSink, _ = borrow.Project(func(value int) (int, error) { return value, nil })
			_ = borrow.Release()
		}
	})
	b.Run("scoped-write", func(b *testing.B) {
		owner, _ := ownership.New(1)
		defer owner.Release()
		b.ReportAllocs()
		for b.Loop() {
			ownershipSink, _ = owner.Mutate(func(value *int) (int, error) { *value++; return *value, nil })
		}
	})
	b.Run("move", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			owner, _ := ownership.New(1)
			moved, _ := owner.Move()
			ownershipSink, _ = moved.Detach()
		}
	})
	b.Run("conflict", func(b *testing.B) {
		owner, _ := ownership.New(1)
		borrow, _ := owner.Borrow()
		defer borrow.Release()
		b.ReportAllocs()
		for b.Loop() {
			if _, err := owner.BorrowMut(); err == nil {
				b.Fatal("BorrowMut succeeded during a read borrow")
			}
		}
	})
}

func BenchmarkNewShared(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		shared, _ := ownership.NewShared(1)
		_ = shared.Release()
	}
}

func BenchmarkShared(b *testing.B) {
	b.Run("clone-release", func(b *testing.B) {
		owner, _ := ownership.New(1)
		shared, _ := owner.IntoShared()
		defer shared.Release()
		b.ReportAllocs()
		for b.Loop() {
			clone, _ := shared.Clone()
			_ = clone.Release()
		}
	})
	b.Run("parallel-read", func(b *testing.B) {
		owner, _ := ownership.New(1)
		shared, _ := owner.IntoShared()
		defer shared.Release()
		b.ReportAllocs()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				clone, _ := shared.Clone()
				ownershipSink, _ = clone.View(func(value int) (int, error) { return value, nil })
				_ = clone.Release()
			}
		})
	})
	b.Run("unwrap", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			owner, _ := ownership.New(1)
			shared, _ := owner.IntoShared()
			owner, _ = shared.IntoOwner()
			ownershipSink, _ = owner.Detach()
		}
	})
}

func BenchmarkBaselines(b *testing.B) {
	b.Run("direct", func(b *testing.B) {
		value := 1
		b.ReportAllocs()
		for b.Loop() {
			ownershipSink = value
		}
	})
	b.Run("rwmutex-read", func(b *testing.B) {
		value := 1
		var mu sync.RWMutex
		b.ReportAllocs()
		for b.Loop() {
			mu.RLock()
			ownershipSink = value
			mu.RUnlock()
		}
	})
	b.Run("rwmutex-write", func(b *testing.B) {
		value := 1
		var mu sync.RWMutex
		b.ReportAllocs()
		for b.Loop() {
			mu.Lock()
			value++
			ownershipSink = value
			mu.Unlock()
		}
	})
}
