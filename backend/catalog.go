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
	// SubscriptionGroup buckets events that overlap on the wire so
	// plugin authors can see at a glance "if I subscribe to X, do I
	// also need Y?" Empty = no overlap concern. Conventions:
	//   - "scoring"          — _GoalScored / _OwnGoal / _TeamScoreChanged
	//                          all fire for the same goal at different
	//                          confidence levels. Pick one.
	//   - "statfeed:catchall" — _StatfeedEvent fires for every variant.
	//   - "statfeed:promoted" — Phase-3 dedicated events (_Save, _Shot…).
	//                          Subscribe to one or the other; both =
	//                          double-firing.
	SubscriptionGroup string `json:"subscriptionGroup,omitempty"`
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
	{Name: "_StatfeedEvent", Category: "stat", Shape: "stat-enriched", LivePhases: liveOrReplay, Desc: "StatfeedEvent with MainTarget/SecondaryTarget pre-resolved against the live roster. NOTE: fires for every Statfeed variant including those with dedicated promoted events (_Save, _Shot, _PlayerDemolished, …). Subscribing to both this and a promoted event will see the same Statfeed twice.", Stability: "provisional", Since: "1.1", SubscriptionGroup: "statfeed:catchall"},
	{Name: "_BallHit", Category: "play", Shape: "ballhit-enriched", LivePhases: liveOnly, Desc: "BallHit with Players[] pre-resolved against the live roster.", Stability: "provisional", Since: "1.1"},
	{Name: "_CrossbarHit", Category: "play", Shape: "crossbar-enriched", LivePhases: liveOnly, Desc: "CrossbarHit with BallLastTouch.Player pre-resolved against the live roster.", Stability: "provisional", Since: "1.1"},
	{Name: "_MatchEnded", Category: "lifecycle", Shape: "matchend-enriched", LivePhases: anyPhase, Desc: "MatchEnded with winnerName, scoreBlue, scoreOrange resolved from the cached final UpdateState.", Stability: "provisional", Since: "1.1"},
	{Name: "_GoalScored", Category: "scoring", Shape: "goal-enriched", LivePhases: liveOrReplay, Desc: "GoalScored with Scorer/Assister/BallLastTouch resolved + scoringTeam/concedingTeam/isOwnGoal flags + same-frame modifiers (aerial/long/turtle/hatTrick/etc.). The isOwnGoal flag is heuristic and synchronous (last-touch on conceding team). For verified own-goal detection use _OwnGoal.", Stability: "provisional", Since: "1.1", SubscriptionGroup: "scoring"},
	{Name: "_OwnGoal", Category: "scoring", Shape: "owngoal", LivePhases: liveOrReplay, Desc: "Score delta verified against the previous tick + opposing-team last touch. Phase-gated to live/replay so off-gameplay score-ups don't false-positive. Higher confidence than _GoalScored.isOwnGoal (which is synchronous and heuristic).", Stability: "provisional", Since: "1.1", SubscriptionGroup: "scoring"},

	// ─── Statfeed promotions (Phase 3) ────────────────────────
	// Each Statfeed variant gets a dedicated _-prefixed event with the
	// targets resolved + per-variant correlation when applicable. The
	// generic _StatfeedEvent above keeps firing as the catch-all — pick
	// one or the other to avoid double-handling.
	{Name: "_PlayerDemolished", Category: "stat", Shape: "demolish", LivePhases: liveOrReplay, Desc: "Demolish with attacker / victim resolved + isSelfDemo / isTeamDemo flags + attackerSpeed / attackerWasSupersonic from the most recent UpdateState (SPECTATOR-only).", Stability: "provisional", Since: "1.1", SubscriptionGroup: "statfeed:promoted"},
	{Name: "_FlipReset", Category: "stat", Shape: "stat-simple", LivePhases: liveOrReplay, Desc: "FlipReset with MainTarget resolved + flipResetsThisMatch counter.", Stability: "provisional", Since: "1.1", SubscriptionGroup: "statfeed:promoted"},
	{Name: "_HatTrick", Category: "stat", Shape: "stat-simple", LivePhases: liveOrReplay, Desc: "HatTrick with MainTarget resolved + goalsThisMatch counter.", Stability: "provisional", Since: "1.1", SubscriptionGroup: "statfeed:promoted"},
	{Name: "_Save", Category: "stat", Shape: "save", LivePhases: liveOrReplay, Desc: "Save with MainTarget resolved + correlatedShot (most recent opposing-team Shot).", Stability: "provisional", Since: "1.1", SubscriptionGroup: "statfeed:promoted"},
	{Name: "_EpicSave", Category: "stat", Shape: "save", LivePhases: liveOrReplay, Desc: "Same as _Save but for the EpicSave variant. Mutually exclusive with _Save on the wire.", Stability: "provisional", Since: "1.1", SubscriptionGroup: "statfeed:promoted"},
	{Name: "_Shot", Category: "stat", Shape: "shot", LivePhases: liveOrReplay, Desc: "Shot with MainTarget resolved + correlatedTouch (same-frame BallHit with pre/post hit speeds).", Stability: "provisional", Since: "1.1", SubscriptionGroup: "statfeed:promoted"},
	{Name: "_Assist", Category: "stat", Shape: "assist", LivePhases: liveOrReplay, Desc: "Assist with MainTarget resolved + correlatedGoal (most recent _GoalScored).", Stability: "provisional", Since: "1.1", SubscriptionGroup: "statfeed:promoted"},
	{Name: "_Center", Category: "stat", Shape: "stat-touch", LivePhases: liveOrReplay, Desc: "Center with MainTarget resolved + correlatedTouch (with pre/post hit speeds).", Stability: "provisional", Since: "1.1", SubscriptionGroup: "statfeed:promoted"},
	{Name: "_Clear", Category: "stat", Shape: "stat-touch", LivePhases: liveOrReplay, Desc: "Clear with MainTarget resolved + correlatedTouch (with pre/post hit speeds).", Stability: "provisional", Since: "1.1", SubscriptionGroup: "statfeed:promoted"},
	{Name: "_BicycleHit", Category: "stat", Shape: "stat-touch", LivePhases: liveOrReplay, Desc: "BicycleHit with MainTarget resolved + correlatedTouch (with pre/post hit speeds). For bicycle-hits that scored, see _GoalScored.modifiers.isBicycleGoal.", Stability: "provisional", Since: "1.1", SubscriptionGroup: "statfeed:promoted"},

	// ─── UpdateState diff events (Phase 4) ────────────────────
	// Synthesized by comparing the current UpdateState against the
	// previous tick. Plugins that subscribe to these get exactly the
	// state change they care about without parsing UpdateState themselves.
	{Name: "_PlayerJoined", Category: "roster", Shape: "player-event", LivePhases: anyPhase, Desc: "Roster diff: player appeared this tick that wasn't in the previous tick. Includes current lifecycle phase so consumers can distinguish lobby joins from mid-match joins.", Stability: "provisional", Since: "1.1"},
	{Name: "_PlayerLeft", Category: "roster", Shape: "player-event", LivePhases: anyPhase, Desc: "Roster diff: player disappeared this tick. Includes current lifecycle phase so consumers can distinguish lobby leaves from mid-match rage-quits.", Stability: "provisional", Since: "1.1"},
	{Name: "_PlayerScoreChanged", Category: "stat", Shape: "score-delta", LivePhases: anyPhase, Desc: "Per-player stat diff (score, goals, assists, saves, shots, touches, demos). Only fields that moved appear in delta.", Stability: "provisional", Since: "1.1"},
	{Name: "_BoostPickup", Category: "play", Shape: "boost-pickup", LivePhases: liveOnly, Desc: "Player's Boost increased between ticks (and not because of a respawn). Boost field is omitted in non-spectator mode for opponents — pickup detection only works for visible players.", Stability: "provisional", Since: "1.1"},
	{Name: "_BallPossessionChanged", Category: "play", Shape: "possession", LivePhases: liveOnly, Desc: "Game.Ball.TeamNum changed between ticks. before/after are nullable — RL's untouched sentinel (255) is normalized to null. Includes triggeredBy (player + pre/post hit speeds) from the most recent BallHit when available.", Stability: "provisional", Since: "1.1"},
	{Name: "_TeamScoreChanged", Category: "scoring", Shape: "team-score-delta", LivePhases: liveOrReplay, Desc: "A team's Score moved between ticks. Catch-all for score changes — fires alongside _GoalScored (regular goals) and _OwnGoal (verified own goals). Subscribe to one of the three depending on the level of detail you need; subscribing to all means every goal is delivered three times.", Stability: "provisional", Since: "1.1", SubscriptionGroup: "scoring"},

	// ─── Match milestones (Phase 4b) ──────────────────────────
	// Once-per-occurrence events that mark notable moments in a match.
	// Each is gated by per-match flags reset on MatchCreated/Destroyed.
	{Name: "_FirstTouch", Category: "play", Shape: "first-touch", LivePhases: liveOnly, Desc: "First BallHit after each RoundStarted (i.e., kickoff first touch). Re-arms every round.", Stability: "provisional", Since: "1.1"},
	{Name: "_FirstBlood", Category: "scoring", Shape: "first-blood", LivePhases: liveOrReplay, Desc: "First _GoalScored of the match. secondsIntoMatch counts from MatchInitialized.", Stability: "provisional", Since: "1.1"},
	{Name: "_OvertimeStarted", Category: "lifecycle", Shape: "overtime-started", LivePhases: liveOnly, Desc: "Rising edge of Game.bOvertime. Fires once per match. Includes tiedAt (the tied score going into OT).", Stability: "provisional", Since: "1.1"},
	{Name: "_DemoChain", Category: "stat", Shape: "demo-chain", LivePhases: liveOrReplay, Desc: "Same attacker registered ≥2 demos within a rolling 5s window. Self-demos and team-demos are excluded. Re-fires with updated count + victims[] for each subsequent demo inside the window.", Stability: "provisional", Since: "1.2"},
	{Name: "_FastestShotOfMatch", Category: "scoring", Shape: "fastest-shot", LivePhases: liveOrReplay, Desc: "Per-match max ball speed (in Unreal Units/second) was surpassed. Sources: BallHit.postHitSpeed and GoalScored.goalSpeed. Re-fires only when the previous max is beaten.", Stability: "provisional", Since: "1.2"},
	// ─── Lifecycle + summary (Phase 5) ────────────────────────
	// Framing-bypass: _MatchState is delivered to every subscriber
	// regardless of the ?events= filter.
	{Name: "_MatchState", Category: "lifecycle", Shape: "match-state", LivePhases: anyPhase, Desc: "Authoritative gameplay state. matchActive (am I in a match) and phase (lobby/countdown/live/paused/replay/ended/podium/none) on every transition, with previousPhase, phaseDurationSeconds, and trigger so subscribers see what changed and why. Replaces the legacy _Lifecycle and _LifecyclePhaseChanged events.", Stability: "stable", Since: "2.0"},
	{Name: "_GoalReplayContext", Category: "replay", Shape: "goal-replay-context", LivePhases: anyPhase, Desc: "Fires on the bReplay rising edge (start of a goal replay) with the full _GoalScored payload (scorer, assister, ballLastTouch, goalSpeed, goalTime, impactLocation, isOwnGoal, modifiers), so plugins know which goal the replay is for without correlating themselves. Edge-detected on UpdateState because recent RL builds skip the discrete GoalReplayStart event.", Stability: "provisional", Since: "1.1"},

	// ─── Discoverability (Phase 6) ────────────────────────────
	// _UnknownStatfeed fires whenever a StatfeedEvent.EventName is not
	// in the verified registry (see verifiedStatfeedNames in
	// statfeed_discoveries.go). The persistent registry at
	// /api/statfeed-discoveries records first/last-seen + counts so
	// new names can be confirmed before being added to the verified set.
	{Name: "_UnknownStatfeed", Category: "stat", Shape: "unknown-statfeed", LivePhases: anyPhase, Desc: "Statfeed.EventName not in the verified registry. Persisted to data/statfeed-discoveries.json for offline review.", Stability: "stable", Since: "1.1"},
}
