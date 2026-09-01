package dedupe_test

import (
	"context"
	"testing"
	"time"

	"github.com/apsis-io/velocity/dedupe"
	"github.com/apsis-io/velocity/ownership"
)

var (
	benchmarkHookKey      int
	benchmarkHookDuration time.Duration
)

func BenchmarkDoBorrowed(b *testing.B) {
	group, err := dedupe.New[int, int]()
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		input, err := ownership.New(1)
		if err != nil {
			b.Fatal(err)
		}
		if _, err := group.DoBorrowed(context.Background(), 1, input, func(_ context.Context, value int) (int, error) { return value, nil }); err != nil {
			b.Fatal(err)
		}
		if err := input.Release(); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDo(b *testing.B) {
	b.Run("no-hooks", func(b *testing.B) {
		benchmarkDo(b, false, dedupe.Hooks[int]{})
	})
	b.Run("nil-hooks", func(b *testing.B) {
		benchmarkDo(b, true, dedupe.Hooks[int]{})
	})
	b.Run("hooks", func(b *testing.B) {
		benchmarkDo(b, true, dedupe.Hooks[int]{
			OnJoin: func(key int, _ bool) { benchmarkHookKey = key },
			OnComplete: func(_ int, duration time.Duration, _ error) {
				benchmarkHookDuration = duration
			},
		})
	})
}

func benchmarkDo(b *testing.B, configure bool, hooks dedupe.Hooks[int]) {
	b.Helper()
	var group *dedupe.Group[int, int]
	var err error
	if configure {
		group, err = dedupe.New[int, int](dedupe.WithHooks[int, int](hooks))
	} else {
		group, err = dedupe.New[int, int]()
	}
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := group.Do(context.Background(), 1, func(context.Context) (int, error) { return 1, nil }); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDoShared(b *testing.B) {
	b.Run("plain-group", func(b *testing.B) {
		group, err := dedupe.New[int, int]()
		if err != nil {
			b.Fatal(err)
		}
		benchmarkDoShared(b, group)
	})
	b.Run("owned-group", func(b *testing.B) {
		group, err := dedupe.New[int, int](dedupe.WithResultDrop[int](func(int) error { return nil }))
		if err != nil {
			b.Fatal(err)
		}
		benchmarkDoShared(b, group)
	})
}

func benchmarkDoShared(b *testing.B, group *dedupe.Group[int, int]) {
	b.Helper()
	b.ReportAllocs()
	for b.Loop() {
		handle, err := group.DoShared(context.Background(), 1, func(context.Context) (int, error) { return 1, nil })
		if err != nil {
			b.Fatal(err)
		}
		if err := handle.Release(); err != nil {
			b.Fatal(err)
		}
	}
}
