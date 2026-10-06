// Copyright (C) 2025-2026 Malformed C. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package converge drives a teleos engine on velocity's runtime. It is the
// operational half of the pair: the engine decides, this package runs the
// loop — one goroutine waiting for level-triggered wakes, observing reality,
// applying effects through a bounded Runner, coalescing repeated effects
// through dedupe.
//
// The split is the mind-and-muscle one. teleos itself imports nothing: the
// engine is a pure pass machine and stays extractable. This package is where
// velocity is allowed in, and everything I/O-shaped lives here — observation,
// effect application, cancellation, the wake channel.
//
// The loop is level-triggered end to end. Run blocks until Wake or context
// cancellation, converges to rest, and blocks again; there is no internal
// periodic rescan, because a poll interval is a clock and clocks belong to
// the caller's daemon, not to a convergence library. A caller whose world
// needs periodic rescan sends Wake from its own ticker — or better, from the
// watches its platform already provides.
package converge

import (
	"context"
	"errors"
	"fmt"

	"github.com/apsis-io/velocity/async"
	"github.com/apsis-io/velocity/dedupe"
	"github.com/apsis-io/velocity/opcodes"
	"github.com/apsis-io/velocity/opruntime"
	"github.com/apsis-io/velocity/teleos"
)

// Dispatch is the apply function for a plan whose effects are opcodes: it
// hands each Instruction to the table registered for its domain. This is the
// velocity-native effect shape — the invariant emits pure, unexecuted data
// (Op plus its operand slots), the table is the only thing that runs them,
// and the handlers themselves are where op composition lives.
//
// The context is accepted and ignored: handlers are synchronous today, and an
// effect that needs cancellation encodes it in the opcode's operand slots or
// carries it in the handler's closure.
func Dispatch(table *opruntime.Table) func(context.Context, opcodes.Instruction) error {
	return func(_ context.Context, inst opcodes.Instruction) error {
		return table.Dispatch(inst)
	}
}

// InstructionKey is the coalescing and oscillation identity for Instruction
// effects: opcode plus operands. Two Instructions that agree on all four
// words are the same effect re-emitted.
func InstructionKey(inst opcodes.Instruction) string {
	return fmt.Sprintf("%d/%d/%d/%d", inst.Op, inst.A, inst.B, inst.C)
}

// Converger is one reconciliation loop: an engine, an observation function, an
// effect applier, and a wake channel. It is safe to Wake from any goroutine;
// Run is the loop's single owner.
type Converger[S, E any] struct {
	engine  *teleos.Engine[S, E]
	observe func(context.Context) (S, error)
	apply   func(context.Context, E) error

	runner   *async.Runner
	coalesce *dedupe.Group[string, struct{}]
	key      func(E) string

	onReport func(context.Context, teleos.Report[S, E])
	onError  func(error)

	wake chan struct{}
}

// Config is everything beyond the required three: the options a daemon sets
// once, at construction. The zero value is a working converger — inline
// effect application, no coalescing, silent failures. It mirrors
// teleos.Config so a loop is configured in one shape end to end.
type Config[S, E any] struct {
	// Runner bounds effect application: a pass's want is applied as one
	// ForEach batch — bounded concurrency, barrier before the next
	// observation, because the next pass must see the effects resolved. Nil
	// applies effects inline, in order, which is what a want list whose
	// effects depend on each other requires anyway.
	Runner *async.Runner

	// Coalesce routes every apply through a shared dedupe group keyed by
	// Key(effect). Its point is convergence across loops: two Convergers
	// whose plans want the same systemd unit share one group, and the second
	// Start joins the first's in-flight application instead of fighting it.
	// Within one Converger the applies are sequential and the group is
	// inert. Key must be the same identity the engine's EffectKey uses, when
	// both are set; a group without a key is rejected at New.
	Coalesce *dedupe.Group[string, struct{}]
	Key      func(E) string

	// OnReport receives every engine report, including the terminal
	// diagnoses — Stalled, Oscillating, Exhausted. A terminal report stops
	// the pass loop until the next Wake, and the engine stays terminal until
	// Reset: the hook is where a daemon logs the diagnosis, alarms, and —
	// once a human has dealt with whatever the diagnosis pointed at — calls
	// Reset and Wake to resume.
	OnReport func(context.Context, teleos.Report[S, E])

	// OnError receives transient failures — an observation that could not be
	// read, an effect that failed. The loop does not die of them: it waits
	// for the next Wake and re-derives everything from the next observation,
	// which is the level-triggered contract. Errors here are diagnostics,
	// not exits.
	OnError func(error)
}

// New builds a converger around an engine. observe reads reality into S —
// the whole world the plan's sensors act on, no more; apply applies one
// effect. Both receive the context Run was called with, and apply is expected
// to be idempotent or coalesced: an effect re-emits on every pass until its
// invariant is satisfied, which is the model working.
func New[S, E any](
	engine *teleos.Engine[S, E],
	observe func(context.Context) (S, error),
	apply func(context.Context, E) error,
	cfg Config[S, E],
) (*Converger[S, E], error) {
	switch {
	case engine == nil:
		return nil, errors.New("converge: nil engine")
	case observe == nil:
		return nil, errors.New("converge: nil observe")
	case apply == nil:
		return nil, errors.New("converge: nil apply")
	case cfg.Coalesce != nil && cfg.Key == nil:
		return nil, errors.New("converge: coalescer without an effect key")
	}

	return &Converger[S, E]{
		engine:   engine,
		observe:  observe,
		apply:    apply,
		runner:   cfg.Runner,
		coalesce: cfg.Coalesce,
		key:      cfg.Key,
		onReport: cfg.OnReport,
		onError:  cfg.OnError,
		wake:     make(chan struct{}, 1),
	}, nil
}

// Wake asks for a pass: something changed, or might have. It never blocks and
// never queues more than one pending pass — a storm of wakes collapses into
// one, and the pass after it re-reads the whole world anyway. Wakes sent
// before Run starts are honored by the first pass.
func (c *Converger[S, E]) Wake() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// Run is the loop: wait for a wake, observe, step, apply, and repeat until
// the engine reports convergence or a terminal diagnosis; then wait again.
// It returns when ctx ends — cancellation is shutdown, not a convergence
// failure, so a context end is a nil return.
func (c *Converger[S, E]) Run(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-c.wake:
		}

		if err := c.pass(ctx); err != nil {
			return err
		}
	}
}

// pass is one wake's worth of convergence: keep stepping until the engine is
// not asking for effects anymore. An observation or effect failure ends the
// pass through onError — the next wake re-derives everything.
func (c *Converger[S, E]) pass(ctx context.Context) error {
	for {
		state, err := c.observe(ctx)
		if err != nil {
			c.fail(err)

			return nil
		}

		report := c.engine.Step(state)
		if c.onReport != nil {
			c.onReport(ctx, report)
		}

		if report.Status != teleos.Frontier {
			return nil
		}

		if err := c.applyWant(ctx, report.Want); err != nil {
			if ctx.Err() != nil {
				return nil
			}

			c.fail(err)

			return nil
		}
	}
}

// applyWant applies one pass's effects. With a Runner the batch is bounded
// and barriers before returning; either way a failure aborts the pass, and
// the next pass re-derives from a fresh observation rather than retrying
// blind.
func (c *Converger[S, E]) applyWant(ctx context.Context, want []E) error {
	if c.runner == nil {
		for _, eff := range want {
			if err := c.applyOne(ctx, eff); err != nil {
				return err
			}
		}

		return nil
	}

	return c.runner.ForEach(ctx, want, func(ctx context.Context, eff E) error {
		return c.applyOne(ctx, eff)
	})
}

// applyOne is one effect through whatever coalescing was configured.
func (c *Converger[S, E]) applyOne(ctx context.Context, eff E) error {
	if c.coalesce == nil {
		return c.apply(ctx, eff)
	}

	_, err := c.coalesce.Do(ctx, c.key(eff), func(context.Context) (struct{}, error) {
		return struct{}{}, c.apply(ctx, eff)
	})

	return err
}

func (c *Converger[S, E]) fail(err error) {
	if c.onError != nil {
		c.onError(err)
	}
}
