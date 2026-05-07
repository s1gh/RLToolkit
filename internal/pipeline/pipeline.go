// Package pipeline orchestrates the two-phase event flow used by the
// backend: every event is first Observed by all StateProcessors, then
// Processed by all EmitProcessors. Synthetic emissions feed downstream
// emit processors in registration order — see Run + runEmit.
package pipeline

import (
	"context"
	"rl-toolkit/internal/bus"
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
// (first phase), then to every EmitProcessor in order (second phase).
// The original event is broadcast first, followed by every synthetic
// emission in the order it was produced.
//
// Emissions feed downstream emit processors. When emit processor N
// returns an event, every later-registered emit processor (N+1, N+2,
// …) also sees that event through its own Process call before the next
// source event is handled. This lets dependent emitters consume
// upstream emissions — e.g. FastestShotEmitter sees the _GoalScored
// produced by GoalEmitter, as long as GoalEmitter is registered first.
//
// Earlier-registered emitters do NOT see emissions from later ones —
// the feed is strictly forward, so registration order encodes the
// dependency graph and there's no risk of cycles or unbounded
// re-entry.
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
// order. Each emission produced by processor i is broadcast and then
// recursively fed into processors i+1..end before the next emission
// from processor i is processed. This produces a strict pre-order
// traversal: the source event's emissions show up in the order
// (producer, producer's children, sibling, sibling's children, …) so
// dependent emitters always see their inputs before their own turn
// comes again.
func (p *Pipeline) runEmit(evt bus.Event, start int, dst Broadcaster) {
	for i := start; i < len(p.emit); i++ {
		for _, out := range p.emit[i].Process(evt) {
			dst.Broadcast(out)
			p.runEmit(out, i+1, dst)
		}
	}
}
