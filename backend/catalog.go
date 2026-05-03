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
}

var anyPhase = []string{} // documents intent: empty = any phase

var liveOrReplay = []string{"live", "replay"}
var liveOnly = []string{"live"}
var liveCountdown = []string{"live", "countdown"}
var liveTickPhases = []string{"live", "replay", "paused", "countdown"}

var EventCatalog = []EventCatalogEntry{
	{Name: "UpdateState", Category: "tick", Shape: "matchstate", LivePhases: liveTickPhases, Desc: "Match snapshot at PacketSendRate (raw envelope payload)."},

	{Name: "GoalScored", Category: "scoring", Shape: "goal", LivePhases: liveOrReplay, Desc: "Scorer + assister + last touch + impact."},
	{Name: "BallHit", Category: "play", Shape: "ballhit", LivePhases: liveOnly, Desc: "Ball touched. Pre/post speed and location."},
	{Name: "CrossbarHit", Category: "play", Shape: "crossbar", LivePhases: liveOnly, Desc: "Ball hit a crossbar."},
	{Name: "StatfeedEvent", Category: "stat", Shape: "stat", LivePhases: liveOrReplay, Desc: "Player earned a stat (demo, save, epic save, etc)."},
	{Name: "ClockUpdatedSeconds", Category: "play", Shape: "clock", LivePhases: liveCountdown, Desc: "Match clock changed by ≥1 second."},

	{Name: "MatchCreated", Category: "lifecycle", Shape: "match", LivePhases: anyPhase, Desc: "All teams replicated; lobby ready."},
	{Name: "MatchInitialized", Category: "lifecycle", Shape: "match", LivePhases: anyPhase, Desc: "First countdown started."},
	{Name: "CountdownBegin", Category: "lifecycle", Shape: "match", LivePhases: anyPhase, Desc: "Round countdown began."},
	{Name: "RoundStarted", Category: "lifecycle", Shape: "match", LivePhases: anyPhase, Desc: "Active gameplay started (countdown ended)."},
	{Name: "MatchPaused", Category: "lifecycle", Shape: "match", LivePhases: anyPhase, Desc: "Match paused by an admin."},
	{Name: "MatchUnpaused", Category: "lifecycle", Shape: "match", LivePhases: anyPhase, Desc: "Match resumed."},
	{Name: "GoalReplayStart", Category: "replay", Shape: "match", LivePhases: anyPhase, Desc: "Goal replay began."},
	{Name: "GoalReplayWillEnd", Category: "replay", Shape: "match", LivePhases: anyPhase, Desc: "Ball exploded during replay (fires only if not skipped)."},
	{Name: "GoalReplayEnd", Category: "replay", Shape: "match", LivePhases: anyPhase, Desc: "Goal replay ended."},
	{Name: "MatchEnded", Category: "lifecycle", Shape: "matchend", LivePhases: anyPhase, Desc: "Match decided. Has WinnerTeamNum."},
	{Name: "PodiumStart", Category: "lifecycle", Shape: "match", LivePhases: anyPhase, Desc: "Game entered podium state."},
	{Name: "MatchDestroyed", Category: "lifecycle", Shape: "match", LivePhases: anyPhase, Desc: "Player left the match."},
	{Name: "ReplayCreated", Category: "lifecycle", Shape: "match", LivePhases: anyPhase, Desc: "Match-history replay loaded (NOT goal replays)."},
}
