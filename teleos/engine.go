// Copyright (C) 2025-2026 Malformed C. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package teleos

import (
	"fmt"
	"strings"
)

// Status is the engine's diagnosis of one step, and of the plan as a whole
// once a terminal status is reached.
type Status int

const (
	// Converged: every invariant in the plan is satisfied.
	Converged Status = iota

	// Frontier: an invariant is unsatisfied; Report.Want holds the effects to
	// apply now. Not terminal — this is the working state.
	Frontier

	// Stalled: the frontier has not moved and the observation has not changed
	// for MaxStalls consecutive passes. Something the plan cannot see is
	// holding reality still. Terminal until Reset.
	Stalled

	// Oscillating: the (frontier, effects) signature repeated two passes back —
	// the plan is fighting itself, applying an effect whose consequence
	// re-opens the gap it just closed. Terminal until Reset.
	Oscillating

	// Exhausted: the pass budget ran out without convergence, or a single pass
	// wanted more effects than MaxEffects — a plan that never stops emitting
	// is a wedge, and a wedge is never handed to an executor. Terminal until
	// Reset.
	Exhausted
)

func (s Status) String() string {
	switch s {
	case Converged:
		return "converged"
	case Frontier:
		return "frontier"
	case Stalled:
		return "stalled"
	case Oscillating:
		return "oscillating"
	case Exhausted:
		return "exhausted"
	}

	return fmt.Sprintf("status(%d)", int(s))
}

// Report is one step's verdict. Want is populated only for Frontier; a
// terminal status names its diagnosis and carries nothing to apply.
type Report[S, E any] struct {
	Status Status

	// Frontier is the index of the unsatisfied invariant in the plan, or -1
	// when converged.
	Frontier int

	// Stage is the name of the unsatisfied invariant, when Config.Names is
	// set; empty otherwise and at convergence. While the frontier is open,
	// Names[:Frontier] is what has already converged; at convergence
	// (Frontier -1) every stage is.
	Stage string

	// Want holds the effects to apply. It is nil for every status except
	// Frontier.
	Want []E

	// Passes and Effects are the engine's running totals: passes consumed and
	// effects emitted since construction or the last Reset.
	Passes  int
	Effects int
}

const (
	defaultMaxEffects = 64
	defaultMaxPasses  = 256
	defaultMaxStalls  = 2
)

// Config builds an Engine. The zero-value fields fall back to their defaults;
// Plan is the one required field.
//
// Same and EffectKey are the two opt-in diagnoses. Same reports whether two
// observations are the same for the plan's purposes — structural equality for
// a comparable S, or a projection onto the fields the plan actually acts on.
// EffectKey names an effect's identity for the oscillation signature; two
// effects with the same key are the same effect re-emitted.
type Config[S, E any] struct {
	// Plan is the ordered invariant list. Slice order is dependency order.
	Plan []Invariant[S, E]

	// Names names the plan's stages, in order, for introspection: a
	// converged-but-silent system says which stage holds the frontier
	// (Report.Stage), and the satisfied prefix — Names[:Report.Frontier] —
	// is the readiness conditions a status endpoint renders, Kubernetes
	// PodConditions style. Empty means the stages are anonymous and the
	// frontier is an index only. When non-empty its length must equal
	// Plan's.
	Names []string

	// MaxEffects bounds one pass's want. Default 64.
	MaxEffects int

	// MaxPasses bounds the passes consumed without convergence. Default 256.
	MaxPasses int

	// MaxStalls bounds the consecutive unmoved passes before Stalled. It has
	// no effect when Same is nil. Default 2.
	MaxStalls int

	// Same reports whether two observations differ for the plan's purposes.
	// Nil disables stall detection.
	Same func(a, b S) bool

	// EffectKey names an effect's identity for the oscillation signature.
	// Nil disables oscillation detection.
	EffectKey func(E) string
}

// Engine is the pass machine over a plan: one Step per observation, effects
// out, diagnosis when the loop stops making progress. It holds no goroutine,
// no timer, and no handle to the world — the caller observes, applies, and
// calls again.
//
// An Engine is not safe for concurrent use: one plan, one loop, one goroutine
// — the reconciliation loop is a singleton by construction, and the caller
// bounds concurrency below this layer (velocity's async.Runner, in the
// reference wiring).
type Engine[S, E any] struct {
	cfg        Config[S, E]
	maxEffects int
	maxPasses  int
	maxStalls  int

	passes    int
	effects   int
	prevState S
	lastFront int
	stalls    int
	sigPrev   string
	sigBack   string
	terminal  *Report[S, E]
}

// New validates the config and returns an engine at pass zero.
func New[S, E any](cfg Config[S, E]) (*Engine[S, E], error) {
	if len(cfg.Plan) == 0 {
		return nil, fmt.Errorf("teleos: plan is empty — an engine with no invariants has nothing to converge")
	}

	if len(cfg.Names) != 0 && len(cfg.Names) != len(cfg.Plan) {
		return nil, fmt.Errorf("teleos: %d stage names for a plan of %d invariants — name every stage or none", len(cfg.Names), len(cfg.Plan))
	}

	for i, inv := range cfg.Plan {
		if inv == nil {
			return nil, fmt.Errorf("teleos: plan entry %d is nil — a nil invariant would panic at the first Step", i)
		}
	}

	e := &Engine[S, E]{cfg: cfg, lastFront: -1}
	e.maxEffects = cfg.MaxEffects

	if e.maxEffects <= 0 {
		e.maxEffects = defaultMaxEffects
	}

	e.maxPasses = cfg.MaxPasses
	if e.maxPasses <= 0 {
		e.maxPasses = defaultMaxPasses
	}

	e.maxStalls = cfg.MaxStalls
	if e.maxStalls <= 0 {
		e.maxStalls = defaultMaxStalls
	}

	return e, nil
}

// Step runs one pass over the observation and returns what to do. The caller
// observes reality into S, applies Report.Want through its executor, observes
// again, and calls again. The engine never touches the world itself.
//
// Once a terminal status is reached, every Step returns the same report
// without re-evaluating — a diagnosis does not un-make itself because the
// daemon kept polling. Reset clears it.
func (e *Engine[S, E]) Step(state S) Report[S, E] {
	if e.terminal != nil {
		return *e.terminal
	}

	if e.passes >= e.maxPasses {
		return e.halt(Report[S, E]{
			Status:   Exhausted,
			Frontier: -1,
			Passes:   e.passes,
			Effects:  e.effects,
		})
	}

	frontier := -1

	var want []E

	for i, inv := range e.cfg.Plan {
		done, w := inv(state)
		if !done {
			frontier, want = i, w

			break
		}
	}

	e.passes++

	if frontier < 0 {
		return e.halt(Report[S, E]{
			Status:   Converged,
			Frontier: -1,
			Passes:   e.passes,
			Effects:  e.effects,
		})
	}

	// A single pass asking for more effects than the budget allows is not an
	// invariant with a long list; it is a wedge. The cap is counted in
	// operations because a wedge does not become harmless by waiting.
	if len(want) > e.maxEffects {
		return e.halt(Report[S, E]{
			Status:   Exhausted,
			Frontier: frontier,
			Passes:   e.passes,
			Effects:  e.effects,
		})
	}

	// Oscillation first: it is the sharper diagnosis, and it fires on a
	// signature from two passes back — A-B-A — because an immediately
	// repeated signature is the healthy case. Reality takes passes to catch
	// up: a unit that is activating re-opens its invariant and re-emits the
	// same effect, and that is convergence working, not fighting. The third
	// clause is what tells those apart: a signature that also matches the
	// immediately previous pass is a repeat, not a return.
	sig := e.signature(frontier, want)
	if e.cfg.EffectKey != nil && sig != "" && sig == e.sigBack && sig != e.sigPrev {
		return e.halt(Report[S, E]{
			Status:   Oscillating,
			Frontier: frontier,
			Passes:   e.passes,
			Effects:  e.effects,
		})
	}

	e.sigBack = e.sigPrev
	e.sigPrev = sig

	// A stall is the opposite failure: the same frontier, the same effects,
	// and an observation that does not move. Something the plan cannot see is
	// holding reality still.
	if e.cfg.Same != nil {
		if frontier == e.lastFront && e.cfg.Same(e.prevState, state) {
			e.stalls++
		} else {
			e.stalls = 0
		}

		if e.stalls >= e.maxStalls {
			return e.halt(Report[S, E]{
				Status:   Stalled,
				Frontier: frontier,
				Passes:   e.passes,
				Effects:  e.effects,
			})
		}
	}

	e.prevState = state
	e.lastFront = frontier
	e.effects += len(want)

	return Report[S, E]{
		Status:   Frontier,
		Frontier: frontier,
		Stage:    e.stageName(frontier),
		Want:     want,
		Passes:   e.passes,
		Effects:  e.effects,
	}
}

// stageName is the named stage at a frontier, or empty when the plan's
// stages are anonymous.
func (e *Engine[S, E]) stageName(frontier int) string {
	if len(e.cfg.Names) == 0 || frontier < 0 || frontier >= len(e.cfg.Names) {
		return ""
	}

	return e.cfg.Names[frontier]
}

// Reset clears a terminal diagnosis and returns the engine to pass zero. Use
// it after whatever the diagnosis was pointing at has been dealt with — a
// human fixed the fight, an administrator moved the stuck resource — never
// on a timer: Reset on a schedule is how a diagnosis gets ignored.
func (e *Engine[S, E]) Reset() {
	e.passes = 0
	e.effects = 0
	e.lastFront = -1
	e.stalls = 0
	e.sigPrev = ""
	e.sigBack = ""

	var zero S

	e.prevState = zero
	e.terminal = nil
}

// halt freezes the engine on a terminal report and returns it. A diagnosis
// that names a frontier names its stage too — an Exhausted or Stalled report
// with an unnamed stage would send the reader counting indices.
func (e *Engine[S, E]) halt(r Report[S, E]) Report[S, E] {
	if r.Frontier >= 0 {
		r.Stage = e.stageName(r.Frontier)
	}

	e.terminal = &r

	return r
}

// signature is the oscillation key: which invariant is open and what it wants.
func (e *Engine[S, E]) signature(frontier int, want []E) string {
	if e.cfg.EffectKey == nil {
		return ""
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%d", frontier)

	for _, eff := range want {
		b.WriteByte('\x00')
		b.WriteString(e.cfg.EffectKey(eff))
	}

	return b.String()
}
