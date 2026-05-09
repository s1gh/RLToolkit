package types

import "strings"

// rlBotPrimaryID is the literal PrimaryId RL ships for every AI player —
// every bot in every match arrives with this same string, distinguished
// only by Name. Without rewriting, downstream code collapses every bot
// into one phantom player.
const rlBotPrimaryID = "Unknown|0|0"

// IsBotID returns true for platform ids that represent AI/bot players.
// Bot ids are minted by CanonicalizeBotID at the wire-decode boundary.
func IsBotID(id string) bool {
	return strings.HasPrefix(id, "Bot|") || strings.HasPrefix(id, "bot|")
}

// CanonicalizeBotID rewrites RL's collision-prone bot id into a stable
// per-bot id of the form "Bot|<sanitized-name>" — Name is the only
// stable per-bot identifier across ticks within a match. Real-player
// ids pass through unchanged. Apply at every UpdateState wire-decode
// site so downstream consumers see one consistent id per bot.
//
// `|` in a name is replaced with `_` to preserve PlatformFromID's
// "everything before the first |" rule. A nameless bot (shouldn't
// happen) keeps Unknown|0|0 — one collision beats coining a new id
// every tick.
func CanonicalizeBotID(primaryID, name string) string {
	if primaryID != rlBotPrimaryID {
		return primaryID
	}
	if name == "" {
		return primaryID
	}
	return "Bot|" + strings.ReplaceAll(name, "|", "_")
}

// PlatformFromID extracts the platform prefix from a PrimaryId
// (everything before the first `|`). Returns "" for empty or malformed
// ids. Used by the roster tracker and the SDK's player enrichment.
func PlatformFromID(id string) string {
	if id == "" {
		return ""
	}
	idx := strings.IndexByte(id, '|')
	if idx <= 0 {
		return ""
	}
	return id[:idx]
}
