package backend

import (
	"bytes"
	"encoding/json"
	"rl-toolkit/internal/types"
	"strings"
)

// EnrichedPlayer + ShortcutRef are aliases over internal/types so the
// canonical wire shapes live in one place. The methods below
// (ResolveByShortcut etc.) hang off RosterTracker, which still lives
// in this package — the aliases let downstream emitters import the
// types without dragging the resolver along.
type ShortcutRef = types.ShortcutRef
type EnrichedPlayer = types.EnrichedPlayer

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
// enriched player using the current live roster. Lookup is by Name —
// RL names are unique within a match. Shortcut is RL's per-match
// slot index (a number), kept on the enriched output for plugins that
// need it but not used for the roster join. Falls back to building a
// minimal enriched stub from the ref itself if no roster match is
// found.
func (r *RosterTracker) ResolveByShortcut(ref ShortcutRef) *EnrichedPlayer {
	if ref.Name == "" {
		return nil
	}

	r.mu.Lock()
	roster := r.lastRoster
	r.mu.Unlock()

	for _, p := range roster {
		if p.Name == ref.Name {
			return r.rosterPlayerToEnriched(p)
		}
	}

	// Fallback: build from the ref itself.
	return &EnrichedPlayer{
		Name: ref.Name,
		Team: ref.TeamNum,
	}
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
			return r.rosterPlayerToEnriched(p)
		}
	}
	out := &EnrichedPlayer{
		ID:       id,
		Platform: platformFromID(id),
		IsBot:    isBotId(id),
	}
	r.stampIsMe(out)
	return out
}

// rosterPlayerToEnriched converts a rosterPlayer to an EnrichedPlayer
// and stamps IsMe via the attached IdentityStore (if any).
func (r *RosterTracker) rosterPlayerToEnriched(p rosterPlayer) *EnrichedPlayer {
	out := &EnrichedPlayer{
		ID:       p.ID,
		Name:     p.Name,
		Team:     p.Team,
		Platform: p.Platform,
		IsBot:    isBotId(p.ID),
	}
	r.stampIsMe(out)
	return out
}

// stampIsMe sets IsMe when the attached IdentityStore's PrimaryID
// matches the player's id. No-op when no store is attached or the
// identity is unset.
func (r *RosterTracker) stampIsMe(p *EnrichedPlayer) {
	if r.identity == nil || p == nil || p.ID == "" {
		return
	}
	if id := r.identity.Get(); id != nil && id.PrimaryID == p.ID {
		p.IsMe = true
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
// Splice strategy: we decode only the inner Data payload, mutate it,
// then splice the re-encoded inner string back into the original raw
// bytes in place of the original Data value. This preserves the outer
// envelope's byte layout — crucially, the position of the "Event" key.
// Going through json.Marshal on the outer envelope reorders keys
// alphabetically (Data before Event); extractEventName scans only the
// first 96 bytes of the wire, so a reordered envelope with a multi-KB
// Data string buried before the Event key would parse as eventName="",
// and Bus.Broadcast would drop the packet for every filtered
// subscriber. Splicing avoids the reorder entirely.
//
// Failure mode: if any decode step fails (malformed envelope,
// unexpected nesting), we return the input unchanged rather than
// dropping the packet. The downstream roster_tracker / synthesizer
// also call canonicalizeBotId on their own decoded copies as a
// defense-in-depth, so a missed rewrite here only affects raw-bus
// subscribers (and even then, only for that one packet).
func rewriteUpdateStateBotIds(raw []byte) []byte {
	if !bytes.Contains(raw, []byte(rlBotPrimaryID)) {
		return raw
	}

	dataStart, dataEnd, dataKey := findDataValueSpan(raw)
	if dataKey == "" {
		return raw
	}
	// dataStart..dataEnd covers the JSON-string token including the
	// surrounding quotes. Decode just that slice to get the inner
	// payload as a Go string.
	var innerStr string
	if err := json.Unmarshal(raw[dataStart:dataEnd], &innerStr); err != nil {
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
	// Re-encode the new inner payload as a JSON string token (quoted,
	// escaped) so it can be spliced back into the outer envelope where
	// the original Data value sat.
	newDataValue, err := json.Marshal(string(innerOut))
	if err != nil {
		return raw
	}

	out := make([]byte, 0, len(raw)+len(newDataValue)-(dataEnd-dataStart))
	out = append(out, raw[:dataStart]...)
	out = append(out, newDataValue...)
	out = append(out, raw[dataEnd:]...)
	return out
}

// findDataValueSpan locates the JSON-string value of the top-level
// "Data" (or "data") field in an RL envelope and returns its byte span
// in raw, plus which casing was found. The returned span includes the
// surrounding quotes so json.Unmarshal can decode it as a string.
//
// Returns key="" when the field isn't present or the envelope can't be
// parsed at the top level. Uses encoding/json's tokenizer to stay
// robust across whitespace, escape sequences, and any extra top-level
// fields RL might add in future builds.
func findDataValueSpan(raw []byte) (start, end int, key string) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil {
		return 0, 0, ""
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return 0, 0, ""
	}
	for dec.More() {
		// Next token is a key (string).
		keyTok, err := dec.Token()
		if err != nil {
			return 0, 0, ""
		}
		k, _ := keyTok.(string)
		// Position of the value: InputOffset is the byte offset *after*
		// the most recent token. We want the start of the value, which
		// the decoder advances to next.
		if k == "Data" || k == "data" {
			// Read the value as RawMessage to capture its byte span.
			before := int(dec.InputOffset())
			var rm json.RawMessage
			if err := dec.Decode(&rm); err != nil {
				return 0, 0, ""
			}
			after := int(dec.InputOffset())
			// Walk forward from `before` (just past the closing quote
			// of the key) past whitespace and the `:` separator to land
			// on the first byte of the value token.
			s := before
			// Skip whitespace before colon.
			for s < after && (raw[s] == ' ' || raw[s] == '\t' || raw[s] == '\n' || raw[s] == '\r') {
				s++
			}
			if s >= after || raw[s] != ':' {
				return 0, 0, ""
			}
			s++ // past ':'
			// Skip whitespace before value.
			for s < after && (raw[s] == ' ' || raw[s] == '\t' || raw[s] == '\n' || raw[s] == '\r') {
				s++
			}
			if s >= after || raw[s] != '"' {
				// Data isn't a JSON-string value — bail.
				return 0, 0, ""
			}
			return s, after, k
		}
		// Skip the value of any other key.
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return 0, 0, ""
		}
	}
	return 0, 0, ""
}
