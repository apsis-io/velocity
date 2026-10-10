// Copyright (C) 2025-2026 Malformed C. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package ownership_test

import (
	"sync/atomic"
	"testing"

	"github.com/apsis-io/velocity/ownership"
)

// The scaling question the single parallel-read number cannot answer: does
// concurrent reading get FASTER with more cores, or does the cell's mutex
// serialize readers so aggregate throughput stays flat? The bench reads a
// Shared value with no clones and no releases — pure read traffic against
// one cell.
//
// Vary the worker count with -cpu (go test -bench . -cpu 1,4,28): the
// testing framework sets GOMAXPROCS per -cpu level and resets it around
// every b.Run, so an in-bench GOMAXPROCS loop cannot vary the worker count —
// every in-bench label would measure the same condition.
func BenchmarkSharedReadScaling(b *testing.B) {
	owner := ownership.Own(1)

	shared, err := owner.IntoShared()
	if err != nil {
		b.Fatal(err)
	}
	defer shared.Release()

	var sink atomic.Int64

	b.ReportAllocs()

	b.RunParallel(func(pb *testing.PB) {
		// Accumulate locally and add once: a shared write per operation
		// would measure cache-line ping-pong on the sink, not the cell.
		local := 0

		for pb.Next() {
			v, _ := shared.View(func(value int) (int, error) { return value, nil })
			local += v
		}

		sink.Add(int64(local))
	})
}
