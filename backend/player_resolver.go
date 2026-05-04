package main

import (
	"bytes"
	"encoding/json"
	"strings"
)

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
// The rewrite uses "Bot|<sanitized-name>" — Name is RL's only stable
// per-bot identifier across ticks within a match (RL keeps bot names
// stable for the duration of a match). A bot named "Roundhouse"
// becomes id="Bot|Roundhouse"; a different bot "Merlin" gets
// id="Bot|Merlin" even though both arrive on the wire with
// PrimaryId="Unknown|0|0".
//
// Why "Bot|" can't collide with a real player: PrimaryId is always
// shaped Platform|UserId|SubId, and Psyonix's platform list
// (Steam/Epic/PS4/XboxOne/Switch/Unknown) doesn't include "Bot".
// A real user's display name could be "Bot" but the id starts with
// the platform, never the display name.
//
// Names are sanitized: any `|` in the bot name would break
// platformFromID's "everything before the first |" rule, so we
// replace it with `_`. Other characters pass through (RL bot names
// are well-formed in practice).
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
	return "Bot|" + strings.ReplaceAll(name, "|", "_")
}

// rewriteUpdateStateBotIds returns a copy of the raw envelope with every
// Players[].PrimaryId rewritten via canonicalizeBotId. Called from the
// RL-client dispatcher before the bus publish, so every downstream
// consumer — bus subscribers, hosted-bus aggregator, direct-mode
// plugins, the SDK's match.build — sees one canonical id per bot.
//
// Fast path: if the wire bytes don't contain the Unknown|0|0 sentinel,
// return the input unchanged. The vast majority of packets in a
// human-only match hit this branch and skip the decode/re-marshal
// allocation entirely.
//
// Failure mode: if the JSON round-trip fails for any reason (malformed
// envelope, unexpected nesting), we return the input unchanged rather
// than dropping the packet. The downstream roster_tracker /
// synthesizer also call canonicalizeBotId on their own decoded copies
// as a defense-in-depth, so a missed rewrite here only affects raw-bus
// subscribers (and even then, only for that one packet).
func rewriteUpdateStateBotIds(raw []byte) []byte {
	if !bytes.Contains(raw, []byte(rlBotPrimaryID)) {
		return raw
	}

	var env map[string]any
	if err := json.Unmarshal(raw, &env); err != nil {
		return raw
	}
	// The envelope's Data field is a JSON-encoded string holding the
	// inner UpdateState payload. Pull either casing.
	var dataKey string
	switch {
	case env["Data"] != nil:
		dataKey = "Data"
	case env["data"] != nil:
		dataKey = "data"
	default:
		return raw
	}
	innerStr, ok := env[dataKey].(string)
	if !ok {
		return raw
	}

	var inner map[string]any
	if err := json.Unmarshal([]byte(innerStr), &inner); err != nil {
		return raw
	}
	// Players[] may be PascalCase or lowercase; only one of the two is
	// present per envelope per RL build.
	var playersKey string
	switch {
	case inner["Players"] != nil:
		playersKey = "Players"
	case inner["players"] != nil:
		playersKey = "players"
	default:
		return raw
	}
	playersAny, ok := inner[playersKey].([]any)
	if !ok {
		return raw
	}
	idKey := "PrimaryId"
	nameKey := "Name"
	if playersKey == "players" {
		idKey = "primaryid"
		nameKey = "name"
	}
	rewrote := false
	for _, pAny := range playersAny {
		p, ok := pAny.(map[string]any)
		if !ok {
			continue
		}
		pid, _ := p[idKey].(string)
		if pid != rlBotPrimaryID {
			continue
		}
		name, _ := p[nameKey].(string)
		if name == "" {
			continue
		}
		p[idKey] = "Bot|" + strings.ReplaceAll(name, "|", "_")
		rewrote = true
	}
	if !rewrote {
		// Sentinel string was somewhere in the bytes but not on a
		// PrimaryId field — return the original to avoid spurious
		// re-marshaling cost.
		return raw
	}

	innerOut, err := json.Marshal(inner)
	if err != nil {
		return raw
	}
	env[dataKey] = string(innerOut)
	out, err := json.Marshal(env)
	if err != nil {
		return raw
	}
	return out
}
