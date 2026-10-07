// Copyright (C) 2025-2026 Malformed C. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package async_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/apsis-io/velocity/async"
)

// These tests pin the tracing pattern the README documents: velocity imports
// no tracer, so spans are the caller's — opened at the call site, carried
// into the work by context transparency, and closed in the Hooks. If a
// refactor breaks context propagation or the hook's completion contract,
// these fail and the documented tracing pattern breaks with them.

type traceKey struct{}

// TestTracingContextReachesEveryTaskShape: the caller opens a span and puts
// it in the context; every collection API's task fn must see it.
func TestTracingContextReachesEveryTaskShape(t *testing.T) {
	runner, err := async.New(async.Limited(4))
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.WithValue(context.Background(), traceKey{}, "span")

	var (
		mu   sync.Mutex
		seen int
	)

	record := func(ctx context.Context) {
		if ctx.Value(traceKey{}) == "span" {
			mu.Lock()
			seen++
			mu.Unlock()
		}
	}

	if _, err := runner.Map(ctx, []int{1, 2, 3}, func(ctx context.Context, i int) (int, error) {
		record(ctx)

		return i, nil
	}); err != nil {
		t.Fatal(err)
	}

	if err := runner.ForEach(ctx, []int{1}, func(ctx context.Context, i int) error {
		record(ctx)

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	f := runner.Submit(ctx, func(ctx context.Context) (int, error) {
		record(ctx)

		return 0, nil
	})

	if _, err := f.Await(context.Background()); err != nil {
		t.Fatal(err)
	}

	if seen != 5 {
		t.Fatalf("the caller's context reached %d of 5 task fns, want all", seen)
	}
}

// TestHooksCarrySpanCompletion pins the contract a tracing caller leans on
// when it closes a task's span in the hook: the hook fires in the task's own
// goroutine, reports the outcome honestly, and precedes the handle's
// resolution — so closing a span there is not racing the awaiter.
func TestHooksCarrySpanCompletion(t *testing.T) {
	hookFired := make(chan async.Hooks, 1)

	runner, err := async.New(async.Limited(1), async.WithHooks(async.Hooks{
		OnTaskComplete: func(index int, label string, waited, duration time.Duration, err error) {
			// A Submit task: not a member of a collection, so no index and
			// no label; uncontended, so no permit wait.
			if index != -1 || label != "" || waited != 0 || err != nil {
				t.Errorf("hook = (%d, %q, %v, %v, %v)", index, label, waited, duration, err)
			}

			hookFired <- async.Hooks{}
		},
	}))
	if err != nil {
		t.Fatal(err)
	}

	f := runner.Submit(context.Background(), func(context.Context) (int, error) {
		time.Sleep(time.Millisecond)

		return 7, nil
	})

	res, err := f.Await(context.Background())
	if err != nil || res.Value != 7 {
		t.Fatalf("Await = (%+v, %v)", res, err)
	}

	select {
	case <-hookFired:
	case <-time.After(5 * time.Second):
		t.Fatal("the hook never fired, before or after the handle resolved")
	}
}
