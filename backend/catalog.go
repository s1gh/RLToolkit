package main

// EventCatalog documents every event the SDK emits to plugins. It is
// served at /api/events for discoverability — plugin authors curl that
// endpoint or browse it from the dashboard to see what's available.
//
// IMPORTANT: keep this in sync with the JS-side `events.catalog` in
// sdk.go (search for "Event catalog"). The JS copy is what plugins
// inspect at runtime via RLT.events.catalog; this Go copy is the
// network-discoverable view. Both are intentionally static — events
// don't appear/disappear, so a build-time mirror is fine.
type EventCatalogEntry struct {
	Name       string   `json:"name"`
	Category   string   `json:"category"`
	Shape      string   `json:"shape"`
	LivePhases []string `json:"live_phases"` // [] means "any phase" (the JS side uses "*")
	Desc       string   `json:"desc"`
	Stability  string   `json:"stability"` // "stable" | "provisional" | "experimental"
	Since      string   `json:"since"`     // "1.0", "1.1", etc.
}

var anyPhase = []string{} // documents intent: empty = any phase

var liveOrReplay = []string{"live", "replay"}
var liveOnly = []string{"live"}
var liveCountdown = []string{"live", "countdown"}
var liveTickPhases = []string{"live", "replay", "paused", "countdown"}

var EventCatalog = []EventCatalogEntry{
	{Name: "UpdateState", Category: "tick", Shape: "matchstate", LivePhases: liveTickPhases, Desc: "Match snapshot at PacketSendRate (raw envelope payload).", Stability: "stable", Since: "1.0"},

	{Name: "GoalScored", Category: "scoring", Shape: "goal", LivePhases: liveOrReplay, Desc: "Scorer + assister + last touch + impact.", Stability: "stable", Since: "1.0"},
	{Name: "BallHit", Category: "play", Shape: "ballhit", LivePhases: liveOnly, Desc: "Ball touched. Pre/post speed and location.", Stability: "stable", Since: "1.0"},
	{Name: "CrossbarHit", Category: "play", Shape: "crossbar", LivePhases: liveOnly, Desc: "Ball hit a crossbar.", Stability: "stable", Since: "1.0"},
	{Name: "StatfeedEvent", Category: "stat", Shape: "stat", LivePhases: liveOrReplay, Desc: "Player earned a stat (demo, save, epic save, etc).", Stability: "stable", Since: "1.0"},
	{Name: "ClockUpdatedSeconds", Category: "play", Shape: "clock", LivePhases: liveCountdown, Desc: "Match clock changed by ≥1 second.", Stability: "stable", Since: "1.0"},

	{Name: "MatchCreated", Category: "lifecycle", Shape: "match", LivePhases: anyPhase, Desc: "All teams replicated; lobby ready.", Stability: "stable", Since: "1.0"},
	{Name: "MatchInitialized", Category: "lifecycle", Shape: "match", LivePhases: anyPhase, Desc: "First countdown started.", Stability: "stable", Since: "1.0"},
	{Name: "CountdownBegin", Category: "lifecycle", Shape: "match", LivePhases: anyPhase, Desc: "Round countdown began.", Stability: "stable", Since: "1.0"},
	{Name: "RoundStarted", Category: "lifecycle", Shape: "match", LivePhases: anyPhase, Desc: "Active gameplay started (countdown ended).", Stability: "stable", Since: "1.0"},
	{Name: "MatchPaused", Category: "lifecycle", Shape: "match", LivePhases: anyPhase, Desc: "Match paused by an admin.", Stability: "stable", Since: "1.0"},
	{Name: "MatchUnpaused", Category: "lifecycle", Shape: "match", LivePhases: anyPhase, Desc: "Match resumed.", Stability: "stable", Since: "1.0"},
	{Name: "GoalReplayStart", Category: "replay", Shape: "match", LivePhases: anyPhase, Desc: "Goal replay began.", Stability: "stable", Since: "1.0"},
	{Name: "GoalReplayWillEnd", Category: "replay", Shape: "match", LivePhases: anyPhase, Desc: "Ball exploded during replay (fires only if not skipped).", Stability: "stable", Since: "1.0"},
	{Name: "GoalReplayEnd", Category: "replay", Shape: "match", LivePhases: anyPhase, Desc: "Goal replay ended.", Stability: "stable", Since: "1.0"},
	{Name: "MatchEnded", Category: "lifecycle", Shape: "matchend", LivePhases: anyPhase, Desc: "Match decided. Has WinnerTeamNum.", Stability: "stable", Since: "1.0"},
	{Name: "PodiumStart", Category: "lifecycle", Shape: "match", LivePhases: anyPhase, Desc: "Game entered podium state.", Stability: "stable", Since: "1.0"},
	{Name: "MatchDestroyed", Category: "lifecycle", Shape: "match", LivePhases: anyPhase, Desc: "Player left the match.", Stability: "stable", Since: "1.0"},
	{Name: "ReplayCreated", Category: "lifecycle", Shape: "match", LivePhases: anyPhase, Desc: "Match-history replay loaded (NOT goal replays).", Stability: "stable", Since: "1.0"},

	// ─── Synthetic events ──────────────────────────────────────
	// _-prefixed events emitted by the toolkit, not by Rocket League.
	// Player references are pre-resolved against the live roster, so
	// subscribers don't need to do their own enrichment.
	{Name: "_StatfeedEvent", Category: "stat", Shape: "stat-enriched", LivePhases: liveOrReplay, Desc: "StatfeedEvent with MainTarget/SecondaryTarget pre-resolved against the live roster.", Stability: "provisional", Since: "1.1"},
	{Name: "_BallHit", Category: "play", Shape: "ballhit-enriched", LivePhases: liveOnly, Desc: "BallHit with Players[] pre-resolved against the live roster.", Stability: "provisional", Since: "1.1"},
	{Name: "_CrossbarHit", Category: "play", Shape: "crossbar-enriched", LivePhases: liveOnly, Desc: "CrossbarHit with BallLastTouch.Player pre-resolved against the live roster.", Stability: "provisional", Since: "1.1"},
	{Name: "_MatchEnded", Category: "lifecycle", Shape: "matchend-enriched", LivePhases: anyPhase, Desc: "MatchEnded with winnerName, scoreBlue, scoreOrange resolved from the cached final UpdateState.", Stability: "provisional", Since: "1.1"},
	{Name: "_GoalScored", Category: "scoring", Shape: "goal-enriched", LivePhases: liveOrReplay, Desc: "GoalScored with Scorer/Assister/BallLastTouch resolved + scoringTeam/concedingTeam/isOwnGoal flags + same-frame modifiers (aerial/long/turtle/hatTrick/etc.).", Stability: "provisional", Since: "1.1"},
	{Name: "_OwnGoal", Category: "scoring", Shape: "owngoal", LivePhases: liveOrReplay, Desc: "Score delta verified against the previous tick + opposing-team last touch. Phase-gated to live/replay so forfeit and mercy-rule score-ups don't false-positive.", Stability: "provisional", Since: "1.1"},

	// ─── Statfeed promotions (Phase 3) ────────────────────────
	// Each Statfeed variant gets a dedicated _-prefixed event with the
	// targets resolved + per-variant correlation when applicable. The
	// generic _StatfeedEvent above keeps firing as the catch-all.
	{Name: "_PlayerDemolished", Category: "stat", Shape: "demolish", LivePhases: liveOrReplay, Desc: "Demolish with attacker / victim resolved + isSelfDemo / isTeamDemo flags.", Stability: "provisional", Since: "1.1"},
	{Name: "_FlipReset", Category: "stat", Shape: "stat-simple", LivePhases: liveOrReplay, Desc: "FlipReset with MainTarget resolved.", Stability: "provisional", Since: "1.1"},
	{Name: "_HatTrick", Category: "stat", Shape: "stat-simple", LivePhases: liveOrReplay, Desc: "HatTrick with MainTarget resolved + goalsThisMatch counter.", Stability: "provisional", Since: "1.1"},
	{Name: "_Save", Category: "stat", Shape: "save", LivePhases: liveOrReplay, Desc: "Save with MainTarget resolved + correlatedShot (most recent opposing-team Shot).", Stability: "provisional", Since: "1.1"},
	{Name: "_EpicSave", Category: "stat", Shape: "save", LivePhases: liveOrReplay, Desc: "Same as _Save but for the EpicSave variant. Mutually exclusive with _Save on the wire.", Stability: "provisional", Since: "1.1"},
	{Name: "_Shot", Category: "stat", Shape: "shot", LivePhases: liveOrReplay, Desc: "Shot with MainTarget resolved + correlatedTouch (same-frame BallHit).", Stability: "provisional", Since: "1.1"},
	{Name: "_Assist", Category: "stat", Shape: "assist", LivePhases: liveOrReplay, Desc: "Assist with MainTarget resolved + correlatedGoal (most recent _GoalScored).", Stability: "provisional", Since: "1.1"},
	{Name: "_Center", Category: "stat", Shape: "stat-touch", LivePhases: liveOrReplay, Desc: "Center with MainTarget resolved + correlatedTouch.", Stability: "provisional", Since: "1.1"},
	{Name: "_Clear", Category: "stat", Shape: "stat-touch", LivePhases: liveOrReplay, Desc: "Clear with MainTarget resolved + correlatedTouch.", Stability: "provisional", Since: "1.1"},
	{Name: "_BicycleHit", Category: "stat", Shape: "stat-touch", LivePhases: liveOrReplay, Desc: "BicycleHit with MainTarget resolved + correlatedTouch.", Stability: "provisional", Since: "1.1"},
}
