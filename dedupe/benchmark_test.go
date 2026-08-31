package dedupe_test

import (
	"context"
	"testing"

	"github.com/apsis-io/velocity/dedupe"
)

func BenchmarkDoNilHooks(b *testing.B) {
	group, err := dedupe.New[int, int](context.Background(), dedupe.WithHooks[int, int](dedupe.Hooks[int]{}))
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		handle, err := group.Do(context.Background(), 1, func(context.Context) (int, error) { return 1, nil })
		if err != nil {
			b.Fatal(err)
		}
		if err := handle.Release(); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDo(b *testing.B) {
	group, err := dedupe.New[int, int](context.Background())
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		handle, err := group.Do(context.Background(), 1, func(context.Context) (int, error) { return 1, nil })
		if err != nil {
			b.Fatal(err)
		}
		if err := handle.Release(); err != nil {
			b.Fatal(err)
		}
	}
}
