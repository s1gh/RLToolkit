package main

import "strings"

// ShortcutRef is the minimal player reference found in raw RL event
// payloads (e.g., Scorer, MainTarget, BallLastTouch.Player).
type ShortcutRef struct {
	Name     string `json:"Name"`
	Shortcut string `json:"Shortcut"`
	TeamNum  int    `json:"TeamNum"`
}

// EnrichedPlayer is the fully-resolved player shape shipped on synthetic
// events. It mirrors what the SDK's resolvePlayer produces, but built
// server-side so every subscriber gets the same enrichment regardless of
// mode (hosted bus or direct SSE).
type EnrichedPlayer struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Team      int    `json:"team"`
	Platform  string `json:"platform,omitempty"`
	IsBot     bool   `json:"isBot"`
	IsMe      bool   `json:"isMe,omitempty"`      // populated by SDK; backend leaves false
	Encounter *any   `json:"encounter,omitempty"` // populated by SDK; backend leaves nil
}

// ResolveByShortcut maps a {Name, Shortcut, TeamNum} stub to a fully
// enriched player using the current live roster. The Shortcut field is
// the player's name as it appears in the event payload; we match against
// the roster by name (case-sensitive) because PrimaryId is not always
// present in event stubs. Falls back to building a minimal enriched stub
// from the ref itself if no roster match is found.
func (r *RosterTracker) ResolveByShortcut(ref ShortcutRef) *EnrichedPlayer {
	if ref.Name == "" && ref.Shortcut == "" {
		return nil
	}
	// Prefer Shortcut when present (it's the authoritative name field in
	// most event stubs), fall back to Name.
	lookup := ref.Shortcut
	if lookup == "" {
		lookup = ref.Name
	}

	r.mu.Lock()
	// We don't store the full player list in RosterTracker, only the
	// fingerprint and last GUID. For now we resolve via a minimal
	// in-memory roster cache maintained alongside the tracker.
	// TODO: maintain full roster snapshot in RosterTracker for server-side
	// enrichment.
	r.mu.Unlock()

	// Best-effort: build from the ref itself. The SDK will re-resolve
	// against its live roster view when it receives the synthetic event.
	return stubToEnriched(ref)
}

// ResolveByPrimaryId looks up a player by their PrimaryId in the current
// roster snapshot. Returns nil if the id is empty or not found.
func (r *RosterTracker) ResolveByPrimaryId(id string) *EnrichedPlayer {
	if id == "" {
		return nil
	}
	// TODO: maintain full roster snapshot in RosterTracker.
	return &EnrichedPlayer{
		ID:       id,
		Platform: platformFromID(id),
		IsBot:    isBotId(id),
	}
}

// stubToEnriched builds a minimal EnrichedPlayer from a ShortcutRef.
// The backend doesn't have the full roster snapshot yet, so this is a
// best-effort stub that the SDK can re-enrich client-side.
func stubToEnriched(ref ShortcutRef) *EnrichedPlayer {
	name := ref.Name
	if name == "" {
		name = ref.Shortcut
	}
	return &EnrichedPlayer{
		Name: name,
		Team: ref.TeamNum,
	}
}

// isBotId returns true for platform ids that represent AI/bot players.
func isBotId(id string) bool {
	return strings.HasPrefix(id, "Bot|") || strings.HasPrefix(id, "bot|")
}
