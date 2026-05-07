// Package emit holds the pipeline's pure synthetic-event producers.
// Each emitter implements pipeline.EmitProcessor (one Process method)
// and stays free of internal mutex state — the pipeline runs them
// single-threaded so any per-match counters live as plain fields.
//
// Cross-package dependencies are expressed as small consumer-side
// interfaces (TickReader, RosterResolver, ReplayGate) so this package
// stays free of any backend coupling.
package emit

import (
	"rl-toolkit/internal/bus"
	"rl-toolkit/internal/types"
)

// RosterResolver is the slim view emitters need into the roster
// tracker: turn a {Name, Shortcut, TeamNum} stub into a fully-enriched
// player. The roster tracker in backend satisfies this structurally.
type RosterResolver interface {
	ResolveByShortcut(ref types.ShortcutRef) *types.EnrichedPlayer
}

// ReplayGate exposes the single bit of TickStore state emitters need
// for phase-gated suppression: are we currently in a goal replay?
// (RL keeps firing some envelopes during the cinematic.)
type ReplayGate interface {
	InReplay() bool
}

// TeamReader is the slim view emitters need into the most recent
// UpdateState's Teams[] cache: pull a team's score / name / colors
// by TeamNum, returning nil if no tick has arrived or the team isn't
// present.
type TeamReader interface {
	TeamByNum(num int) *types.TeamRef
}

// payloadBytes returns the JSON object the upstream emitter produced.
// During the synth-bridged transition the legacy producer ships flat
// raw JSON (with "Event" at the top level inline with the fields), so
// Raw is what we want. Native emit processors return Event{Name, Data}
// where Data is the typed payload — when that's set we prefer it.
func payloadBytes(evt bus.Event) []byte {
	if len(evt.Data) > 0 {
		return evt.Data
	}
	return evt.Raw
}
