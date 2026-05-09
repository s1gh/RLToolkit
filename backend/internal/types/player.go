// Package types holds the shared wire shapes that cross package
// boundaries — the player reference RL ships and the enriched player
// the toolkit emits. Kept here so emit/source/state/server packages
// can depend on them without dragging in the resolver, roster
// tracker, or identity store.
package types

// ShortcutRef is the minimal player reference found in raw RL event
// payloads (e.g., Scorer, MainTarget, BallLastTouch.Player). Shortcut
// is RL's per-match player slot index (0..N), shipped as a JSON number
// — decoding it as a string silently fails the whole inner Decode of
// any event carrying it.
type ShortcutRef struct {
	Name     string `json:"Name"`
	Shortcut int    `json:"Shortcut"`
	TeamNum  int    `json:"TeamNum"`
}

// EnrichedPlayer is the fully-resolved player shape shipped on
// synthetic events. It mirrors what the SDK's resolvePlayer produces,
// but built server-side so every subscriber gets the same enrichment
// regardless of mode (hosted bus or direct SSE).
//
// Encounter (per-user "have I seen this player before?") is the
// SDK's concern — it has the persistence layer for the ledger. The
// backend doesn't try to populate it.
type EnrichedPlayer struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Team     int    `json:"team"`
	Platform string `json:"platform,omitempty"`
	IsBot    bool   `json:"isBot"`
	IsMe     bool   `json:"isMe,omitempty"`
}
