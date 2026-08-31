package benchmarks_test

import (
	"context"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/apsis-io/velocity/dedupe"
	samber "github.com/samber/go-singleflightx"
	xsingleflight "golang.org/x/sync/singleflight"
	janos "resenje.org/singleflight"
)

func BenchmarkDedupe(b *testing.B) {
	b.Run("velocity/uncontended", benchmarkVelocityUncontended)
	b.Run("janos/uncontended", benchmarkJanosUncontended)
	b.Run("samber/uncontended", benchmarkSamberUncontended)
	b.Run("x-sync/uncontended", benchmarkXSyncUncontended)
	b.Run("velocity/contended", benchmarkVelocityContended)
	b.Run("janos/contended", benchmarkJanosContended)
	b.Run("samber/contended", benchmarkSamberContended)
	b.Run("x-sync/contended", benchmarkXSyncContended)
}

func benchmarkVelocityUncontended(b *testing.B) {
	group, err := dedupe.New[string, int](context.Background())
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		handle, err := group.Do(context.Background(), "key", func(context.Context) (int, error) { return 1, nil })
		if err != nil {
			b.Fatal(err)
		}
		_ = handle.Release()
	}
}

func benchmarkJanosUncontended(b *testing.B) {
	var group janos.Group[string, int]
	b.ReportAllocs()
	for b.Loop() {
		_, _, err := group.Do(context.Background(), "key", func(context.Context) (int, error) { return 1, nil })
		if err != nil {
			b.Fatal(err)
		}
	}
}

func benchmarkSamberUncontended(b *testing.B) {
	var group samber.Group[string, int]
	b.ReportAllocs()
	for b.Loop() {
		_, err, _ := group.Do("key", func() (int, error) { return 1, nil })
		if err != nil {
			b.Fatal(err)
		}
	}
}

func benchmarkXSyncUncontended(b *testing.B) {
	var group xsingleflight.Group
	b.ReportAllocs()
	for b.Loop() {
		_, err, _ := group.Do("key", func() (any, error) { return 1, nil })
		if err != nil {
			b.Fatal(err)
		}
	}
}

func benchmarkVelocityContended(b *testing.B) {
	group, err := dedupe.New[string, int](context.Background())
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			handle, err := group.Do(context.Background(), "key", func(context.Context) (int, error) { return 1, nil })
			if err != nil {
				b.Error(err)
				continue
			}
			_ = handle.Release()
		}
	})
}

func benchmarkJanosContended(b *testing.B) {
	var group janos.Group[string, int]
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _, err := group.Do(context.Background(), "key", func(context.Context) (int, error) { return 1, nil })
			if err != nil {
				b.Error(err)
			}
		}
	})
}

func benchmarkSamberContended(b *testing.B) {
	var group samber.Group[string, int]
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, err, _ := group.Do("key", func() (int, error) { return 1, nil })
			if err != nil {
				b.Error(err)
			}
		}
	})
}

func benchmarkXSyncContended(b *testing.B) {
	var group xsingleflight.Group
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, err, _ := group.Do("key", func() (any, error) { return 1, nil })
			if err != nil {
				b.Error(err)
			}
		}
	})
}

func velocityBackends() []struct {
	name string
	opt  dedupe.Option[string, int]
} {
	return []struct {
		name string
		opt  dedupe.Option[string, int]
	}{
		{name: "mutex", opt: dedupe.WithMutexBackend[string, int]()},
		{name: "xsync", opt: dedupe.WithXsyncBackend[string, int]()},
		// Eight shards spread independent keys without making each shard tiny.
		{name: "sharded-8", opt: dedupe.WithSharded[string, int](8)},
	}
}

func benchmarkBackends(b *testing.B, run func(*testing.B, *dedupe.Group[string, int])) {
	b.Helper()
	for _, tc := range velocityBackends() {
		b.Run(tc.name, func(b *testing.B) {
			group, err := dedupe.New[string, int](context.Background(), tc.opt)
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			run(b, group)
		})
	}
}

// Backends are measured across all three workloads deliberately. Measuring
// only the contended distinct-key case would show the default in its best
// light and hide that the mutex backend wins uncontended.

func BenchmarkVelocityBackendsUncontended(b *testing.B) {
	benchmarkBackends(b, func(b *testing.B, group *dedupe.Group[string, int]) {
		for b.Loop() {
			handle, err := group.Do(context.Background(), "key", func(context.Context) (int, error) { return 1, nil })
			if err != nil {
				b.Fatal(err)
			}
			_ = handle.Release()
		}
	})
}

func BenchmarkVelocityBackendsSharedKey(b *testing.B) {
	benchmarkBackends(b, func(b *testing.B, group *dedupe.Group[string, int]) {
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				handle, err := group.Do(context.Background(), "key", func(context.Context) (int, error) { return 1, nil })
				if err != nil {
					b.Error(err)
					continue
				}
				_ = handle.Release()
			}
		})
	})
}

func BenchmarkVelocityBackendsContended(b *testing.B) {
	benchmarkBackends(b, func(b *testing.B, group *dedupe.Group[string, int]) {
		var nextKey atomic.Int64
		b.RunParallel(func(pb *testing.PB) {
			key := strconv.FormatInt(nextKey.Add(1), 10)
			for pb.Next() {
				handle, err := group.Do(context.Background(), key, func(context.Context) (int, error) { return 1, nil })
				if err != nil {
					b.Error(err)
					continue
				}
				_ = handle.Release()
			}
		})
	})
}
