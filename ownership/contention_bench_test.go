// Copyright (C) 2025-2026 Malformed C. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package ownership_test

import (
	"sync/atomic"
	"testing"

	"github.com/apsis-io/velocity/ownership"
)

// The contention question the read-scaling bench cannot answer: what does
// useful throughput look like when readers and writers collide on one cell?
// Scoped access reports ErrConflict rather than queueing, so a bench that
// alternates View and Mutate blindly would mostly measure the error path.
// Instead every iteration retries until its operation SUCCEEDS — one bench
// operation is one successful View or Mutate, and the reported ns/op is the
// cost of useful work under mutual conflict-retry. Failed attempts are not
// free; their cost shows up as allocations from the conflict errors and in
// the ns/op itself.
//
// The mixes are per-operation, by stride within each worker: writeEvery=4
// makes every fourth operation of every worker a write, so the mix holds no
// matter how the runtime distributes work across goroutines.
//
// Vary the worker count with -cpu (go test -bench . -cpu 1,4,28): the
// testing framework sets GOMAXPROCS per -cpu level and resets it around
// every b.Run, so an in-bench GOMAXPROCS loop cannot vary the worker count.
func BenchmarkSharedContention(b *testing.B) {
	shared, err := ownership.Own(0).IntoShared()
	if err != nil {
		b.Fatal(err)
	}
	defer shared.Release()

	var sink atomic.Int64

	mixes := []struct {
		name       string
		writeEvery int // 0 = never write; else a write when i%writeEvery == 0
	}{
		{"writes=0%", 0},
		{"writes=12.5%", 8},
		{"writes=25%", 4},
		{"writes=50%", 2},
		{"writes=100%", 1},
	}

	for _, mix := range mixes {
		b.Run(mix.name, func(b *testing.B) {
			b.ReportAllocs()

			b.RunParallel(func(pb *testing.PB) {
				i := 0
				// Read results accumulate locally and land in the sink
				// once per worker: a shared write per operation would
				// measure cache-line ping-pong on the sink, not the cell.
				// The writes' *v++ stays shared — mutating the value is
				// the workload.
				local := 0

				for pb.Next() {
					if mix.writeEvery != 0 && i%mix.writeEvery == 0 {
						for {
							if err := shared.WithWrite(func(v *int) error { *v++; return nil }); err == nil {
								break
							}
						}
					} else {
						for {
							v, err := shared.View(func(value int) (int, error) { return value, nil })
							if err == nil {
								local += v
								break
							}
						}
					}

					i++
				}

				sink.Add(int64(local))
			})
		})
	}
}
