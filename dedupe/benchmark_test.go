package dedupe_test

import (
	"context"
	"testing"
	"time"

	"github.com/apsis-io/velocity/dedupe"
)

var (
	benchmarkHookKey      int
	benchmarkHookDuration time.Duration
)

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
		group, err = dedupe.New[int, int](context.Background(), dedupe.WithHooks[int, int](hooks))
	} else {
		group, err = dedupe.New[int, int](context.Background())
	}
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
