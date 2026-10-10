// Copyright (C) 2025-2026 Malformed C. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pool_test

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/apsis-io/velocity/pool"
)

// The pool's shared-lock question: what does Get/Release churn cost when
// workers contend for one warm pool? The capacity permit is a channel and
// never blocks here (Max comfortably exceeds any -cpu level), so this
// measures the pool lock, the idle slice, and the checkout machinery — not
// capacity waiting, which is the channel's job and a different question.
//
// Vary the worker count with -cpu (go test -bench . -cpu 1,4,28): the
// testing framework sets GOMAXPROCS per -cpu level and resets it around
// every b.Run, so an in-bench GOMAXPROCS loop cannot vary the worker count.
func BenchmarkPoolReuse(b *testing.B) {
	ctx := context.Background()

	var sink atomic.Int64

	p := pool.Must(pool.New(pool.Config[int]{
		New: func(context.Context) (int, error) { return 1, nil },
		Max: 64,
	}))
	defer p.Close()

	b.ReportAllocs()

	b.RunParallel(func(pb *testing.PB) {
		// Local accumulation: a shared write per operation would measure
		// cache-line ping-pong, not the pool.
		local := 0

		for pb.Next() {
			c, err := p.Get(ctx)
			if err != nil {
				b.Error(err)

				return
			}

			v, err := c.Value()
			if err != nil {
				b.Error(err)

				return
			}

			local += v

			if err := c.Release(); err != nil {
				b.Error(err)

				return
			}
		}

		sink.Add(int64(local))
	})
}
