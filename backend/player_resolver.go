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

// RosterSnapshot returns the most recent roster seen by the tracker.
// Safe for concurrent use; returns a copy of the internal slice.
func (r *RosterTracker) RosterSnapshot() []rosterPlayer {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]rosterPlayer, len(r.lastRoster))
	copy(out, r.lastRoster)
	return out
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
	roster := r.lastRoster
	r.mu.Unlock()

	// Search the roster by name. RL names are unique within a match, so
	// the first hit is the right player.
	for _, p := range roster {
		if p.Name == lookup {
			return rosterPlayerToEnriched(p)
		}
	}

	// Fallback: build from the ref itself.
	return stubToEnriched(ref)
}

// ResolveByPrimaryId looks up a player by their PrimaryId in the current
// roster snapshot. Returns nil if the id is empty or not found.
func (r *RosterTracker) ResolveByPrimaryId(id string) *EnrichedPlayer {
	if id == "" {
		return nil
	}

	r.mu.Lock()
	roster := r.lastRoster
	r.mu.Unlock()

	for _, p := range roster {
		if p.ID == id {
			return rosterPlayerToEnriched(p)
		}
	}
	return &EnrichedPlayer{
		ID:       id,
		Platform: platformFromID(id),
		IsBot:    isBotId(id),
	}
}

// rosterPlayerToEnriched converts a rosterPlayer to an EnrichedPlayer.
func rosterPlayerToEnriched(p rosterPlayer) *EnrichedPlayer {
	return &EnrichedPlayer{
		ID:       p.ID,
		Name:     p.Name,
		Team:     p.Team,
		Platform: p.Platform,
		IsBot:    isBotId(p.ID),
	}
}

// stubToEnriched builds a minimal EnrichedPlayer from a ShortcutRef when
// the player is not found in the live roster.
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
// Bot ids are minted by canonicalizeBotId at the wire-decode boundary;
// see that function for why we don't trust RL's raw bot id directly.
func isBotId(id string) bool {
	return strings.HasPrefix(id, "Bot|") || strings.HasPrefix(id, "bot|")
}

// rlBotPrimaryID is the literal PrimaryId Rocket League ships for every
// AI player. RL doesn't differentiate bots in the wire — every bot in
// every match arrives with this same string, with only the Name field
// distinguishing them. Without rewriting, downstream code (encounter
// ledger, roster diffs, synthetic-event resolution) collapses every
// bot into one phantom player.
const rlBotPrimaryID = "Unknown|0|0"

// canonicalizeBotId rewrites RL's collision-prone bot id into a stable
// per-bot id derived from the Name. Real-player ids pass through
// unchanged. Apply at every UpdateState wire-decode site so every
// downstream consumer sees one consistent id per bot.
//
// The rewrite uses "Bot|<name>" — Name is RL's only stable per-bot
// identifier across ticks within a match (RL keeps bot names stable
// for the duration of a match). A bot named "Roundhouse" becomes
// id="Bot|Roundhouse"; a different bot "Merlin" gets id="Bot|Merlin"
// even though both arrive on the wire with PrimaryId="Unknown|0|0".
//
// If RL ships a bot with no name (shouldn't happen but defensive),
// the original Unknown|0|0 is preserved — better one collision than
// silently coining a new "Bot|" id every tick.
func canonicalizeBotId(primaryID, name string) string {
	if primaryID != rlBotPrimaryID {
		return primaryID
	}
	if name == "" {
		return primaryID
	}
	return "Bot|" + name
}
