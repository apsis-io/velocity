// Copyright (C) 2025-2026 Malformed C. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package dedupe_test

import (
	"context"
	"testing"
	"time"

	"github.com/apsis-io/velocity/dedupe"
)

// These tests pin the tracing contract the README documents for dedupe: an
// execution OUTLIVES its callers, so its context derives from the group's
// base context and not from whichever caller led the round — which makes a
// joining caller's span a LINK to the execution's span, never its parent.
// The Hooks are the execution's span lifecycle: OnJoin (with its leader
// flag) is where a joiner links, OnComplete is the End.

type traceKey struct{}

func TestTracingExecutionRunsUnderTheBaseNotTheCaller(t *testing.T) {
	// OnJoin is the join gate: the leader holds its execution open until the
	// joiner has actually joined — releasing earlier would let the round
	// complete before the join arrives, and the joiner would lead a fresh
	// round instead of linking to this one.
	joins := make(chan bool, 2)

	group, err := dedupe.New[string, string](
		dedupe.WithBaseContext[string, string](
			context.WithValue(context.Background(), traceKey{}, "base"),
		),
		dedupe.WithHooks[string, string](dedupe.Hooks[string]{
			OnJoin: func(_ string, leader bool) { joins <- leader },
		}),
	)
	if err != nil {
		t.Fatal(err)
	}

	var (
		fnCtx     context.Context
		release   = make(chan struct{})
		joinerErr = make(chan error, 1)
	)

	// The leader's own context carries a caller value that must NOT reach
	// fn: the work does not belong to the caller that led the round.
	leaderCtx := context.WithValue(context.Background(), traceKey{}, "leader-caller")

	go group.Do(leaderCtx, "key", func(ctx context.Context) (string, error) {
		fnCtx = ctx

		<-release

		return "v", nil
	})

	if leader := <-joins; !leader {
		t.Fatal("first OnJoin reported leader=false")
	}

	// A joiner mid-flight: its Do returns when the leader's execution ends.
	joinerCtx := context.WithValue(context.Background(), traceKey{}, "joiner-caller")

	go func() {
		_, err := group.Do(joinerCtx, "key", func(context.Context) (string, error) {
			panic("a joiner never runs fn")
		})

		joinerErr <- err
	}()

	if leader := <-joins; leader {
		t.Fatal("second OnJoin reported leader=true, want the joiner")
	}

	close(release)

	if err := <-joinerErr; err != nil {
		t.Fatal(err)
	}

	if got, _ := fnCtx.Value(traceKey{}).(string); got != "base" {
		t.Fatalf("fn's context carried %q, want the base's \"base\" — caller values are the callers' spans, and the execution is nobody's child", got)
	}
}

func TestHooksReportJoinAndCompletion(t *testing.T) {
	// OnJoin gates the round: the leader's fn holds until the joiner has
	// actually joined, so the test never races the round's completion.
	joins := make(chan bool, 2)

	var onCompleteDur time.Duration

	onComplete := make(chan struct{}, 1)

	group, err := dedupe.New[string, string](dedupe.WithHooks[string, string](dedupe.Hooks[string]{
		OnJoin: func(_ string, leader bool) { joins <- leader },
		OnComplete: func(_ string, duration time.Duration, err error) {
			onCompleteDur = duration

			onComplete <- struct{}{}
		},
	}))
	if err != nil {
		t.Fatal(err)
	}

	joinerErr := make(chan error, 1)

	release := make(chan struct{})

	go group.Do(context.Background(), "key", func(context.Context) (string, error) {
		<-release // hold the round open for the join

		return "v", nil
	})

	if leader := <-joins; !leader {
		t.Fatal("first OnJoin reported leader=false")
	}

	go func() {
		_, jerr := group.Do(context.Background(), "key", func(context.Context) (string, error) {
			panic("joiner never runs fn")
		})

		joinerErr <- jerr
	}()

	// The joiner links: OnJoin fires with leader false.
	if leader := <-joins; leader {
		t.Fatal("second OnJoin reported leader=true, want the joiner")
	}

	close(release)

	if err := <-joinerErr; err != nil {
		t.Fatal(err)
	}

	// The round's End: OnComplete once, for the key, with the outcome.
	select {
	case <-onComplete:
		if onCompleteDur < 0 {
			t.Fatalf("OnComplete duration = %v", onCompleteDur)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("OnComplete never fired")
	}
}
