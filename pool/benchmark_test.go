package pool_test

import (
	"context"
	"testing"

	"github.com/apsis-io/velocity/pool"
)

func BenchmarkGetRelease(b *testing.B) {
	p, err := pool.New(pool.Config[int]{
		New: func(context.Context) (int, error) { return 1, nil },
		Max: 8,
	})
	if err != nil {
		b.Fatal(err)
	}
	defer p.Close()
	ctx := context.Background()
	b.Run("sequential", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			c, err := p.Get(ctx)
			if err != nil {
				b.Fatal(err)
			}
			_ = c.Release()
		}
	})
	b.Run("parallel", func(b *testing.B) {
		b.ReportAllocs()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				c, err := p.Get(ctx)
				if err != nil {
					b.Fatal(err)
				}
				_ = c.Release()
			}
		})
	})
}
