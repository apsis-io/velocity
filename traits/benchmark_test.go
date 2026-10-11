package traits_test

import (
	"context"
	"testing"

	"github.com/apsis-io/velocity/traits"
)

var benchmarkInt int

func BenchmarkDrop(b *testing.B) {
	direct := func(value int) error { benchmarkInt = value; return nil }

	composed, err := traits.ComposeDrops(direct, direct, direct)
	if err != nil {
		b.Fatal(err)
	}

	b.Run("direct", func(b *testing.B) {
		b.ReportAllocs()

		for b.Loop() {
			_ = direct(1)
		}
	})
	b.Run("composed", func(b *testing.B) {
		b.ReportAllocs()

		for b.Loop() {
			_ = composed(1)
		}
	})
}

func BenchmarkClone(b *testing.B) {
	direct := func(value int) (int, error) { return value + 1, nil }

	composed, err := traits.ComposeClones(direct, direct, direct)
	if err != nil {
		b.Fatal(err)
	}

	drop := traits.Drop[int](func(int) error { return nil })

	withDrop, err := drop.Clone(direct, direct, direct)
	if err != nil {
		b.Fatal(err)
	}

	b.Run("direct", func(b *testing.B) {
		b.ReportAllocs()

		for b.Loop() {
			benchmarkInt, _ = direct(1)
		}
	})
	b.Run("composed", func(b *testing.B) {
		b.ReportAllocs()

		for b.Loop() {
			benchmarkInt, _ = composed(1)
		}
	})
	b.Run("composed-with-drop", func(b *testing.B) {
		b.ReportAllocs()

		for b.Loop() {
			benchmarkInt, _ = withDrop(1)
		}
	})
}

func BenchmarkFutureCompleteTry(b *testing.B) {
	f := traits.NewFuture[int]()

	b.ReportAllocs()

	for b.Loop() {
		f.Complete(7, nil)

		if res, ok := f.Try(); !ok || res.Value != 7 {
			b.Fatal("Try after Complete failed")
		}
	}
}

func BenchmarkFutureCompleteAwaitResolved(b *testing.B) {
	f := traits.NewFuture[int]()
	ctx := context.Background()

	b.ReportAllocs()

	for b.Loop() {
		f.Complete(7, nil)

		res, err := f.Await(ctx)
		if err != nil || res.Value != 7 {
			b.Fatal("Await after Complete failed")
		}
	}
}
