// Package pipeline orchestrates the two-phase event flow used by the
// backend: every event is first Observed by all StateProcessors, then
// Processed by all EmitProcessors. Synthetic emissions feed downstream
// emit processors in registration order — see Run + runEmit.
package pipeline

import (
	"context"
	"rl-toolkit/backend/internal/bus"
)

// Source produces Events. The RL TCP client is the production source;
// tests use a slice-backed fake.
type Source interface {
	Events(ctx context.Context) <-chan bus.Event
}

// Broadcaster ships events to subscribers. The SSE bus is the
// production implementation.
type Broadcaster interface {
	Broadcast(evt bus.Event)
}

// StateProcessor consumes events to maintain queryable state. Runs in
// the pipeline's first phase, before any EmitProcessor sees the event.
// Does NOT emit events itself.
type StateProcessor interface {
	Observe(evt bus.Event)
}

// EmitProcessor consumes events and returns zero or more synthetic
// events to broadcast. Runs in the pipeline's second phase, after every
// state processor has Observed the event. Pure: same input + state →
// same output, no side effects beyond updating its own internal state.
type EmitProcessor interface {
	Process(evt bus.Event) []bus.Event
}

// Pipeline orchestrates the two-phase event flow.
type Pipeline struct {
	state []StateProcessor
	emit  []EmitProcessor
}

// New creates an empty pipeline. Register processors via AddState and
// AddEmit; call Run to start.
func New() *Pipeline {
	return &Pipeline{}
}

// AddState registers a state processor. Order of registration is
// preserved across Observe calls.
func (p *Pipeline) AddState(sp StateProcessor) { p.state = append(p.state, sp) }

// AddEmit registers an emit processor. Order of registration is
// preserved across Process calls; emissions from earlier processors
// are broadcast before later ones for the same input event.
func (p *Pipeline) AddEmit(ep EmitProcessor) { p.emit = append(p.emit, ep) }

// Run drives the pipeline until the source channel closes or ctx is
// canceled. Each event from src goes to every StateProcessor in order
// (first phase), then through runEmit's strict-forward emit chain
// (second phase). The original event is broadcast before any synthetic
// emissions.
func (p *Pipeline) Run(ctx context.Context, src Source, dst Broadcaster) {
	ch := src.Events(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case evt, ok := <-ch:
			if !ok {
				return
			}
			for _, sp := range p.state {
				sp.Observe(evt)
			}
			dst.Broadcast(evt)
			p.runEmit(evt, 0, dst)
		}
	}
}

// runEmit feeds evt into every emit processor at index >= start, in
// order. Each emission is broadcast and recursively fed into
// processors i+1..end before the next emission from processor i runs.
// This is a strict-forward pre-order traversal: dependent emitters
// always see their inputs before their own turn, and registration
// order encodes the dependency graph (no cycles possible).
func (p *Pipeline) runEmit(evt bus.Event, start int, dst Broadcaster) {
	for i := start; i < len(p.emit); i++ {
		for _, out := range p.emit[i].Process(evt) {
			dst.Broadcast(out)
			p.runEmit(out, i+1, dst)
		}
	}
}
