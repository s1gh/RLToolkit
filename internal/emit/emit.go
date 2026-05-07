// Package emit holds the pipeline's pure synthetic-event producers.
// Each emitter implements pipeline.EmitProcessor (one Process method)
// and stays free of internal mutex state — the pipeline runs them
// single-threaded so any per-match counters live as plain fields.
package emit

import "rl-toolkit/internal/bus"

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
