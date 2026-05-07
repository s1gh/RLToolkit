package types

import "strings"

// rlBotPrimaryID is the literal PrimaryId Rocket League ships for every
// AI player. RL doesn't differentiate bots in the wire — every bot in
// every match arrives with this same string, with only the Name field
// distinguishing them. Without rewriting, downstream code (encounter
// ledger, roster diffs, synthetic-event resolution) collapses every
// bot into one phantom player.
const rlBotPrimaryID = "Unknown|0|0"

// IsBotID returns true for platform ids that represent AI/bot players.
// Bot ids are minted by CanonicalizeBotID at the wire-decode boundary;
// see that function for why we don't trust RL's raw bot id directly.
func IsBotID(id string) bool {
	return strings.HasPrefix(id, "Bot|") || strings.HasPrefix(id, "bot|")
}

// CanonicalizeBotID rewrites RL's collision-prone bot id into a stable
// per-bot id derived from the Name. Real-player ids pass through
// unchanged. Apply at every UpdateState wire-decode site so every
// downstream consumer sees one consistent id per bot.
//
// The rewrite uses "Bot|<sanitized-name>" — Name is RL's only stable
// per-bot identifier across ticks within a match. A bot named
// "Roundhouse" becomes id="Bot|Roundhouse"; a different bot "Merlin"
// gets id="Bot|Merlin" even though both arrive on the wire with
// PrimaryId="Unknown|0|0".
//
// Names are sanitized: any `|` in the bot name would break
// PlatformFromID's "everything before the first |" rule, so we
// replace it with `_`. Other characters pass through (RL bot names
// are well-formed in practice).
//
// If RL ships a bot with no name (shouldn't happen but defensive),
// the original Unknown|0|0 is preserved — better one collision than
// silently coining a new "Bot|" id every tick.
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
