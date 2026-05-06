package main

import (
	"context"
	"encoding/json"
)

// Event is the unit of data flowing through the pipeline. Decoded
// envelope, raw payload kept for processors that want to skip
// per-field decoding.
type Event struct {
	// Name is the canonical PascalCase event name. Synthetic events
	// produced by processors carry the "_"-prefixed convention
	// (_MatchState, _GoalScored, ...).
	Name string
	// Data is the event-specific payload. For RL events, it's the
	// "Data" field of the envelope. For synthetic events, it's the
	// marshaled struct the emitting processor produced.
	Data json.RawMessage
	// Raw is the original wire bytes. Present for events read from a
	// Source; nil for synthetic events emitted by processors.
	Raw []byte
}

// Source produces Events. The RL TCP client is the production source;
// tests use a slice-backed fake.
type Source interface {
	Events(ctx context.Context) <-chan Event
}

// Broadcaster ships events to subscribers. The SSE bus is the
// production implementation.
type Broadcaster interface {
	Broadcast(evt Event)
}

// StateProcessor consumes events to maintain queryable state. Runs in
// the pipeline's first phase, before any EmitProcessor sees the event.
// Does NOT emit events itself.
type StateProcessor interface {
	Observe(evt Event)
}

// EmitProcessor consumes events and returns zero or more synthetic
// events to broadcast. Runs in the pipeline's second phase, after every
// state processor has Observed the event. Pure: same input + state →
// same output, no side effects beyond updating its own internal state.
type EmitProcessor interface {
	Process(evt Event) []Event
}

// Pipeline orchestrates the two-phase event flow.
type Pipeline struct {
	state []StateProcessor
	emit  []EmitProcessor
}

func NewPipeline() *Pipeline {
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
// (first phase), then to every EmitProcessor in order (second phase);
// the original event followed by all synthetic emissions are broadcast
// to dst in the order they were produced.
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
			for _, ep := range p.emit {
				for _, out := range ep.Process(evt) {
					dst.Broadcast(out)
				}
			}
		}
	}
}
