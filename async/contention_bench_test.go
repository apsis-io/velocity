// Copyright (C) 2025-2026 Malformed C. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package async_test

import (
	"context"
	"testing"

	"github.com/apsis-io/velocity/async"
)

// The uncontended benches — BenchmarkSemaphoreAcquire and BenchmarkMutexLock
// — measure one worker cycling a free lock. The contention question, what
// the primitive costs when workers actually collide, is the one the pool's
// channel permit answered at three times the throughput, so these churn the
// same primitives from many workers. The semaphore's capacity comfortably
// exceeds any -cpu level, so capacity never binds and the bench measures
// the acquire path, not capacity waiting; the mutex is binary and fully
// contended by design.
//
// Vary the worker count with -cpu (go test -bench . -cpu 1,4,28): the
// testing framework sets GOMAXPROCS per -cpu level and resets it around
// every b.Run, so an in-bench GOMAXPROCS loop cannot vary the worker count.
// And only interleaved rounds count — a single baseline has twice this
// session measured a cliff that controlled rounds could not reproduce.

func BenchmarkSemaphoreChurn(b *testing.B) {
	sem, err := async.NewSemaphore(64)
	if err != nil {
		b.Fatal(err)
	}

	ctx := context.Background()

	b.ReportAllocs()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			permit, err := sem.Acquire(ctx)
			if err != nil {
				b.Error(err)

				return
			}

			permit.Release()
		}
	})
}

func BenchmarkMutexChurn(b *testing.B) {
	mu := async.NewMutex()
	ctx := context.Background()

	b.ReportAllocs()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			held, err := mu.Lock(ctx)
			if err != nil {
				b.Error(err)

				return
			}

			held.Release()
		}
	})
}
