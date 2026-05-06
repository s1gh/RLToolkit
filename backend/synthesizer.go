package main

import (
	"encoding/json"
	"sync"
	"time"
)

// Synthesizer turns raw RL events into _-prefixed synthetic events with
// pre-resolved player references and other server-side enrichment. One
// instance is attached to the RL client alongside the trackers; Feed is
// called from the dispatcher after the roster tracker has digested the
// envelope, so player resolution sees the up-to-date roster.
type Synthesizer struct {
	bus    Broadcaster
	roster *RosterTracker
	// matchState is the unified gameplay-state machine. Consulted by
	// emitters that should only fire during real gameplay (e.g. _OwnGoal
	// — a score-delta during a forfeit or mercy rule must not flag as
	// an own goal) and by diff emitters that need to skip replays /
	// post-match screens. Optional; nil disables phase gating, which is
	// what the unit tests do.
	matchState *MatchState

	// discoveries is the persistent registry of unknown Statfeed names.
	// Optional — when set, _UnknownStatfeed observations bump the
	// per-name counter and the /api/statfeed-discoveries endpoint can
	// return them.
	discoveries *StatfeedDiscoveryStore

	// ticks is the shared per-tick decode store. Owned by main; the
	// synth's diff emitters read prev/latest from it instead of
	// caching their own copy.
	ticks *TickStore

	// statfeed is the bridge to the extracted StatfeedEmitter. The
	// synth's onGoalScored uses it to consume the per-player
	// flipReset arm when stamping IsFlipResetGoal. Goes away when
	// GoalEmitter extracts.
	statfeed *StatfeedEmitter

// correlation is the shared sliding window of recent events. Owned
	// by main; processors share one instance so a producer (e.g.
	// BallHitEmitter) can record a touch and a consumer (the synth's
	// _GoalScored fallback, soon emit_own_goal) can look it back up.
	// Emitters like _GoalScored use it to look back for modifier
	// Statfeeds (AerialGoal, BackwardsGoal,
	// …) and _OwnGoal can look back for the GoalScored it belongs to.
	// Sized in events, not ticks — see CorrelationBuffer.
	correlation *CorrelationBuffer

// lastGoalMu guards lastGoal. Set when _GoalScored is published,
	// read when GoalReplayStart arrives so _GoalReplayStarted can ship
	// the resolved goal payload without depending on the shared
	// correlation buffer (which can evict the entry under burst load
	// from intervening BallHit/StatfeedEvent records). Holds the full
	// enriched envelope so _GoalReplayStarted can mirror every field
	// _GoalScored carried (assister, ballLastTouch, modifiers, ...).
	// Cleared on match boundaries via resetMatchMilestones.
	lastGoalMu sync.Mutex
	lastGoal   *enrichedGoalScored

	// realGoalsMu guards realGoalsByID. Counts honest (non-own-goal)
	// goals per player. The synth still owns the counter while
	// onGoalScored lives here; StatfeedEmitter reads it via the
	// RealGoals method on this struct for HatTrick suppression. Moves
	// to OwnGoalEmitter when Goal extracts.
	realGoalsMu   sync.Mutex
	realGoalsByID map[string]int
}

func NewSynthesizer(bus Broadcaster, roster *RosterTracker, correlation *CorrelationBuffer, ticks *TickStore) *Synthesizer {
	return &Synthesizer{
		bus:           bus,
		roster:        roster,
		correlation:   correlation,
		ticks:         ticks,
		realGoalsByID: make(map[string]int),
	}
}

// SetSummaryTimings is a no-op kept for test compatibility while we
// finish removing the remaining _MatchSummary call sites.
func (s *Synthesizer) SetSummaryTimings(endedTimeout, podiumSettle time.Duration) {
	_ = endedTimeout
	_ = podiumSettle
}

// AttachMatchState wires the unified gameplay-state machine so emitters
// that should only fire during real gameplay can gate on its current
// phase, and diff emitters can skip replays / post-match phases (RL
// keeps streaming UpdateStates with stale boost / possession / score
// data into both, and we don't want to publish synthetic events for
// that phantom activity). Call before Run.
func (s *Synthesizer) AttachMatchState(m *MatchState) { s.matchState = m }

// RealGoals returns the per-match honest-goal count for `playerID`
// (own goals don't count). Satisfies the realGoalsLookup interface so
// StatfeedEmitter can suppress _HatTrick when RL's count includes own
// goals. Will move to OwnGoalEmitter once Goal extracts and the
// counter ownership shifts per the design spec.
func (s *Synthesizer) RealGoals(playerID string) int {
	if playerID == "" {
		return 0
	}
	s.realGoalsMu.Lock()
	defer s.realGoalsMu.Unlock()
	return s.realGoalsByID[playerID]
}

// AttachStatfeedEmitter wires the new StatfeedEmitter so the synth's
// onGoalScored can clear its flipReset arm via the emitter (which now
// owns the flag). Removed when GoalEmitter extracts.
func (s *Synthesizer) AttachStatfeedEmitter(e *StatfeedEmitter) { s.statfeed = e }

// AttachDiscoveryStore wires the persistent unknown-Statfeed registry.
// Optional — without it, _UnknownStatfeed still publishes but no entry
// is persisted to disk and the /api/statfeed-discoveries endpoint
// returns an empty list.
func (s *Synthesizer) AttachDiscoveryStore(store *StatfeedDiscoveryStore) {
	s.discoveries = store
}

// teamRef is the slice of UpdateState.Game.Teams[] we cache for downstream
// enrichment. Score is here for upcoming Phase-4 diff events; only Name
// and TeamNum are read by _MatchEnded.
type teamRef struct {
	TeamNum        int    `json:"teamNum"`
	Name           string `json:"name"`
	Score          int    `json:"score"`
	ColorPrimary   string `json:"colorPrimary,omitempty"`
	ColorSecondary string `json:"colorSecondary,omitempty"`
}

// Feed inspects each envelope and dispatches to the per-event synthesizer.
// Cheap when the event isn't one we synthesize (single name compare).
func (s *Synthesizer) Feed(raw []byte) {
	name := extractEventName(raw)
	switch name {
	case "UpdateState":
		s.onUpdateState(raw)
	case "GoalScored":
		s.onGoalScored(raw)
	case "MatchCreated", "MatchDestroyed":
		s.resetMatchMilestones()
	}
}

// resetMatchMilestones clears every per-match flag. Called on
// MatchCreated (new match starting) and MatchDestroyed (back to menu).
// Without this, a back-to-back rematch into the same lobby would
// silently skip _FirstBlood because firstBloodFired was still true.
func (s *Synthesizer) resetMatchMilestones() {
	// Drop the cached goal so the first GoalReplayStart of a new match
	// can't pick up the previous match's last goal.
	s.lastGoalMu.Lock()
	s.lastGoal = nil
	s.lastGoalMu.Unlock()
	// Reset per-player real-goal counters. Without this a player who
	// scored 2 honest goals last match would only need 1 more this
	// match to false-trigger _HatTrick.
	s.realGoalsMu.Lock()
	for k := range s.realGoalsByID {
		delete(s.realGoalsByID, k)
	}
	s.realGoalsMu.Unlock()
}

// tickSnapshot is the slice of UpdateState we cache for diff emitters.
// Only fields read by Phase-4 events are kept — the full envelope is
// big and we don't need most of it.
type tickSnapshot struct {
	matchGUID  string
	teams      []teamRef
	players    []tickPlayer
	ballTeam   int  // Game.Ball.TeamNum; 255 = untouched
	hasBall    bool // false when Game.Ball was absent
	overtime   bool
	bReplay    bool // Game.bReplay; rising edge marks a goal replay starting
}

// tickPlayer is the per-player slice of UpdateState we cache.
type tickPlayer struct {
	id          string
	name        string
	team        int
	score       int
	goals       int
	assists     int
	saves       int
	shots       int
	touches     int
	carTouches  int
	demos       int
	boost       *int // pointer because RL omits in non-spectator mode
	demolished  bool
	onGround    bool
	speed       *float64 // pointer: SPECTATOR-only field, omitted otherwise
	supersonic  bool
}

// updateStateFull mirrors the wire shape we need for Phase-4 diffs.
// Same case-tolerant pattern as the rest of the synthesizer: accept
// PascalCase or lowercase top-level keys.
type updateStateFull struct {
	MatchGUID    string                  `json:"MatchGuid"`
	MatchGUIDLow string                  `json:"matchguid"`
	Game         *updateStateGame        `json:"Game"`
	GameLow      *updateStateGame        `json:"game"`
	Players      []updateStateFullPlayer `json:"Players"`
	PlayersLow   []updateStateFullPlayer `json:"players"`
}

type updateStateGame struct {
	Teams     []wireTeam `json:"Teams"`
	Ball      *struct {
		TeamNum int `json:"TeamNum"`
	} `json:"Ball"`
	BOvertime bool `json:"bOvertime"`
	BReplay   bool `json:"bReplay"`
}

type updateStateFullPlayer struct {
	PrimaryID    string   `json:"PrimaryId"`
	Name         string   `json:"Name"`
	TeamNum      int      `json:"TeamNum"`
	Score        int      `json:"Score"`
	Goals        int      `json:"Goals"`
	Assists      int      `json:"Assists"`
	Saves        int      `json:"Saves"`
	Shots        int      `json:"Shots"`
	Touches      int      `json:"Touches"`
	CarTouches   int      `json:"CarTouches"`
	Demos        int      `json:"Demos"`
	Boost        *int     `json:"Boost"`
	Speed        *float64 `json:"Speed"`
	BDemolished  bool     `json:"bDemolished"`
	BOnGround    bool     `json:"bOnGround"`
	BSupersonic  bool     `json:"bSupersonic"`
}

// onUpdateState runs the diff emitters owned by the synthesizer:
// _PlayerJoined / _PlayerLeft, _PlayerScoreChanged, _BoostPickup,
// _BallPossessionChanged, _TeamScoreChanged, plus _OwnGoal. Reads the
// snapshot pair from the shared TickStore — the parse already
// happened in TickStore.Observe before this synth-bridged emit
// processor ran.
func (s *Synthesizer) onUpdateState(raw []byte) {
	curr := s.ticks.Latest()
	prev := s.ticks.Previous()
	if curr == nil {
		return
	}

	if prev == nil {
		// First tick: no diffs to compute.
		return
	}

	// Different-match guard: when the match guid changes, every
	// "diff" against the previous snapshot would be misleading. The
	// new tick is a fresh baseline.
	if prev.matchGUID != "" && curr.matchGUID != "" && prev.matchGUID != curr.matchGUID {
		return
	}

	// Replay-edge detection runs regardless — _GoalReplayStarted is
	// the one event that's *supposed* to fire on entering replay.
	s.diffReplayEdge(prev, curr)

	// Roster + per-player score diffs run in any phase. Players can
	// join/leave or have stats reconciled in lobby, podium, etc.
	s.diffPlayers(prev, curr)

	// Play-state diffs only make sense during active gameplay. During
	// replay the ball/cars move on screen but nothing is actually
	// happening; on the post-match screen RL keeps streaming
	// UpdateStates with stale flags. Both would otherwise emit phantom
	// _BoostPickup / _BallPossessionChanged / _TeamScoreChanged /
	// _OvertimeStarted events.
	if s.matchState != nil {
		ph := s.matchState.Snapshot().Phase
		if ph != PhaseLive && ph != PhaseCountdown && ph != PhasePaused {
			return
		}
	}

	s.diffPlayersLive(prev, curr)
	s.diffTeamScores(prev, curr)
	s.diffBallPossession(prev, curr)
}

// diffReplayEdge emits _GoalReplayStarted on a rising bReplay edge.
// Recent RL builds skip the discrete GoalReplayStart event entirely, so
// the bReplay flag on the per-tick Game snapshot is the only reliable
// "goal replay began" signal. We mirror the LifecycleTracker's edge
// detection here so the synthetic event fires on the same builds the
// rest of the toolkit already supports.
func (s *Synthesizer) diffReplayEdge(prev, curr *tickSnapshot) {
	if prev.bReplay || !curr.bReplay {
		return
	}
	s.lastGoalMu.Lock()
	cached := s.lastGoal
	s.lastGoalMu.Unlock()
	if cached == nil {
		return
	}
	// Mirror every field _GoalScored published, just renamed at the
	// wire level so subscribers can switch on Event. Copy by value so
	// downstream mutations of the cached envelope (none today, but
	// defensive) don't reach back into the source.
	out := *cached
	out.Event = "_GoalReplayStarted"
	out.MatchGUID = curr.matchGUID
	if b, err := json.Marshal(out); err == nil {
		s.bus.Broadcast(Event{Raw: b})
	}
}

// diffPlayers walks the previous + current player lists and emits
// _PlayerJoined / _PlayerLeft on roster identity changes,
// _PlayerScoreChanged on stat-field deltas, and _BoostPickup on a
// rising boost edge during live play.
// diffPlayers emits roster changes (_PlayerJoined / _PlayerLeft).
// Roster identity moves can land in any phase (a player can drop
// during a goal replay), so this isn't phase-gated. Per-stat and
// play-state diffs live in diffPlayersLive.
func (s *Synthesizer) diffPlayers(prev, curr *tickSnapshot) {
	prevByID := make(map[string]*tickPlayer, len(prev.players))
	for i := range prev.players {
		p := &prev.players[i]
		if p.id != "" {
			prevByID[p.id] = p
		}
	}
	currByID := make(map[string]*tickPlayer, len(curr.players))
	for i := range curr.players {
		p := &curr.players[i]
		if p.id != "" {
			currByID[p.id] = p
		}
	}

	// _PlayerJoined: ids in curr but not in prev.
	for id, p := range currByID {
		if _, was := prevByID[id]; was {
			continue
		}
		s.emitPlayerJoined(curr.matchGUID, p)
	}
	// _PlayerLeft: ids in prev but not in curr.
	for id, p := range prevByID {
		if _, still := currByID[id]; still {
			continue
		}
		s.emitPlayerLeft(prev.matchGUID, p)
	}
}

// diffPlayersLive emits the play-state player diffs that only make
// sense during active gameplay:
//   - _PlayerScoreChanged: RL keeps streaming stats during goal
//     replays; the goal-frame delta already fired live, so any
//     replay-tick deltas are spurious reconciliation noise.
//   - _BoostPickup: cars on screen during replay also "pick up boost"
//     cosmetically; we don't want that.
//   - flip-reset bookkeeping: used by goal modifiers that only fire
//     during live play.
func (s *Synthesizer) diffPlayersLive(prev, curr *tickSnapshot) {
	prevByID := make(map[string]*tickPlayer, len(prev.players))
	for i := range prev.players {
		p := &prev.players[i]
		if p.id != "" {
			prevByID[p.id] = p
		}
	}
	for i := range curr.players {
		c := &curr.players[i]
		if c.id == "" {
			continue
		}
		p, ok := prevByID[c.id]
		if !ok {
			continue
		}
		s.emitPlayerScoreChangedIfDelta(curr.matchGUID, p, c)
		s.emitBoostPickupIfRising(curr.matchGUID, p, c)
		if !p.onGround && c.onGround && s.statfeed != nil {
			// Touched ground — clear any flip-reset arm so the next
			// goal from this player isn't tagged unless they earn
			// another reset run.
			s.statfeed.ClearFlipResetArm(c.id)
		}
	}
}

// emitPlayerJoined publishes a _PlayerJoined envelope. The current
// gameplay phase is stamped on the wire so consumers can distinguish
// lobby joins from mid-match joins without their own phase
// subscription. Empty when no LifecycleTracker is attached.
func (s *Synthesizer) emitPlayerJoined(guid string, p *tickPlayer) {
	if p == nil || p.id == "" {
		return
	}
	enriched := &EnrichedPlayer{
		ID:       p.id,
		Name:     p.name,
		Team:     p.team,
		Platform: platformFromID(p.id),
		IsBot:    isBotId(p.id),
	}
	out := struct {
		Event     string          `json:"Event"`
		MatchGUID string          `json:"matchGuid,omitempty"`
		Player    *EnrichedPlayer `json:"player"`
		Phase     string          `json:"phase,omitempty"`
	}{
		Event:     "_PlayerJoined",
		MatchGUID: guid,
		Player:    enriched,
		Phase:     s.currentPhaseString(),
	}
	if b, err := json.Marshal(out); err == nil {
		s.bus.Broadcast(Event{Raw: b})
	}
}

func (s *Synthesizer) emitPlayerLeft(guid string, p *tickPlayer) {
	if p == nil || p.id == "" {
		return
	}
	enriched := &EnrichedPlayer{
		ID:       p.id,
		Name:     p.name,
		Team:     p.team,
		Platform: platformFromID(p.id),
		IsBot:    isBotId(p.id),
	}
	out := struct {
		Event     string          `json:"Event"`
		MatchGUID string          `json:"matchGuid,omitempty"`
		Player    *EnrichedPlayer `json:"player"`
		Phase     string          `json:"phase,omitempty"`
	}{
		Event:     "_PlayerLeft",
		MatchGUID: guid,
		Player:    enriched,
		Phase:     s.currentPhaseString(),
	}
	if b, err := json.Marshal(out); err == nil {
		s.bus.Broadcast(Event{Raw: b})
	}
}

// currentPhaseString returns the current MatchState phase as a
// wire-friendly string, or "" when no MatchState is attached.
func (s *Synthesizer) currentPhaseString() string {
	if s.matchState == nil {
		return ""
	}
	return string(s.matchState.Snapshot().Phase)
}

// emitPlayerScoreChangedIfDelta compares the seven non-spectator stat
// fields. Only sends a delta map for fields that actually moved, so
// subscribers can branch on (e.g.) `delta.demos > 0` without checking
// for the field's existence.
func (s *Synthesizer) emitPlayerScoreChangedIfDelta(guid string, prev, curr *tickPlayer) {
	delta := map[string]int{}
	if curr.score != prev.score {
		delta["score"] = curr.score - prev.score
	}
	if curr.goals != prev.goals {
		delta["goals"] = curr.goals - prev.goals
	}
	if curr.assists != prev.assists {
		delta["assists"] = curr.assists - prev.assists
	}
	if curr.saves != prev.saves {
		delta["saves"] = curr.saves - prev.saves
	}
	if curr.shots != prev.shots {
		delta["shots"] = curr.shots - prev.shots
	}
	if curr.touches != prev.touches {
		delta["touches"] = curr.touches - prev.touches
	}
	if curr.demos != prev.demos {
		delta["demos"] = curr.demos - prev.demos
	}
	if len(delta) == 0 {
		return
	}
	enriched := &EnrichedPlayer{
		ID:       curr.id,
		Name:     curr.name,
		Team:     curr.team,
		Platform: platformFromID(curr.id),
		IsBot:    isBotId(curr.id),
	}
	out := struct {
		Event     string          `json:"Event"`
		MatchGUID string          `json:"matchGuid,omitempty"`
		Player    *EnrichedPlayer `json:"player"`
		Delta     map[string]int  `json:"delta"`
	}{
		Event:     "_PlayerScoreChanged",
		MatchGUID: guid,
		Player:    enriched,
		Delta:     delta,
	}
	if b, err := json.Marshal(out); err == nil {
		s.bus.Broadcast(Event{Raw: b})
	}
}

// emitBoostPickupIfRising fires when the player's Boost increased
// (i.e., they picked up a pad or ran over a big-boost icon). Suppress
// the post-respawn case (demolished → not demolished) — that's a boost
// reset, not a pickup. Also suppress the first observation (no
// baseline), which happens when prev.boost is nil (non-spectator
// blackout) — we have no idea whether the value moved.
func (s *Synthesizer) emitBoostPickupIfRising(guid string, prev, curr *tickPlayer) {
	if prev.boost == nil || curr.boost == nil {
		return
	}
	if *curr.boost <= *prev.boost {
		return
	}
	if prev.demolished && !curr.demolished {
		// Respawn boost-reset, not a pickup.
		return
	}
	enriched := &EnrichedPlayer{
		ID:       curr.id,
		Name:     curr.name,
		Team:     curr.team,
		Platform: platformFromID(curr.id),
		IsBot:    isBotId(curr.id),
	}
	out := struct {
		Event       string          `json:"Event"`
		MatchGUID   string          `json:"matchGuid,omitempty"`
		Player      *EnrichedPlayer `json:"player"`
		BoostBefore int             `json:"boostBefore"`
		BoostAfter  int             `json:"boostAfter"`
		Delta       int             `json:"delta"`
	}{
		Event:       "_BoostPickup",
		MatchGUID:   guid,
		Player:      enriched,
		BoostBefore: *prev.boost,
		BoostAfter:  *curr.boost,
		Delta:       *curr.boost - *prev.boost,
	}
	if b, err := json.Marshal(out); err == nil {
		s.bus.Broadcast(Event{Raw: b})
	}
}

// diffTeamScores fires _TeamScoreChanged when any team's Score moves.
// Distinct from _OwnGoal: this fires for every score delta, including
// regular goals.
func (s *Synthesizer) diffTeamScores(prev, curr *tickSnapshot) {
	prevByNum := make(map[int]int, len(prev.teams))
	for _, t := range prev.teams {
		prevByNum[t.TeamNum] = t.Score
	}
	for _, t := range curr.teams {
		old, ok := prevByNum[t.TeamNum]
		if !ok || t.Score == old {
			continue
		}
		out := struct {
			Event     string `json:"Event"`
			MatchGUID string `json:"matchGuid,omitempty"`
			TeamNum   int    `json:"teamNum"`
			TeamName  string `json:"teamName,omitempty"`
			Before    int    `json:"before"`
			After     int    `json:"after"`
			Delta     int    `json:"delta"`
		}{
			Event:     "_TeamScoreChanged",
			MatchGUID: curr.matchGUID,
			TeamNum:   t.TeamNum,
			TeamName:  t.Name,
			Before:    old,
			After:     t.Score,
			Delta:     t.Score - old,
		}
		if b, err := json.Marshal(out); err == nil {
			s.bus.Broadcast(Event{Raw: b})
		}
	}
}

// diffBallPossession fires _BallPossessionChanged when the ball's
// TeamNum field changes. Normalize 255 (RL's "untouched" sentinel) to
// null in the JSON via *int.
func (s *Synthesizer) diffBallPossession(prev, curr *tickSnapshot) {
	if !prev.hasBall || !curr.hasBall {
		return
	}
	if prev.ballTeam == curr.ballTeam {
		return
	}
	toNullable := func(team int) *int {
		if team == 255 {
			return nil
		}
		t := team
		return &t
	}
	out := struct {
		Event       string                   `json:"Event"`
		MatchGUID   string                   `json:"matchGuid,omitempty"`
		Before      *int                     `json:"before"`
		After       *int                     `json:"after"`
		TriggeredBy *enrichedCorrelatedTouch `json:"triggeredBy,omitempty"`
	}{
		Event:       "_BallPossessionChanged",
		MatchGUID:   curr.matchGUID,
		Before:      toNullable(prev.ballTeam),
		After:       toNullable(curr.ballTeam),
		TriggeredBy: recentTouch(s.correlation, 3),
	}
	if b, err := json.Marshal(out); err == nil {
		s.bus.Broadcast(Event{Raw: b})
	}
}

// ownGoalScoreAfter is part of _OwnGoal's wire shape; defined here so
// emit_own_goal.go can reuse the type alongside the synthesizer (the
// summary writer also reads scoreAfter values via teamScore below).
type ownGoalScoreAfter struct {
	Blue   int `json:"blue"`
	Orange int `json:"orange"`
}

// teamScore reads the Score for a TeamNum out of a Teams[] snapshot,
// returning 0 if the team isn't present.
func teamScore(teams []teamRef, num int) int {
	for _, t := range teams {
		if t.TeamNum == num {
			return t.Score
		}
	}
	return 0
}

type gameTeams struct {
	Teams []wireTeam `json:"Teams"`
}

type wireTeam struct {
	TeamNum        int    `json:"TeamNum"`
	Name           string `json:"Name"`
	Score          int    `json:"Score"`
	ColorPrimary   string `json:"ColorPrimary"`
	ColorSecondary string `json:"ColorSecondary"`
}

// lookupTickScalars returns Speed and bSupersonic for the player with
// the given canonical id, from the most recent cached UpdateState. Speed
// is nil when no tick is cached, the player isn't in the tick, or the
// field was omitted (non-spectator). The boolean is meaningful only
// when the speed pointer is non-nil — see the supersonic-stamping
// guard in emitPlayerDemolished.
func (s *Synthesizer) lookupTickScalars(id string) (*float64, bool) {
	return s.ticks.PlayerScalars(id)
}

// teamByNum returns a copy of the cached team with the given TeamNum,
// or nil if no UpdateState has populated the cache or the team isn't
// present.
func (s *Synthesizer) teamByNum(num int) *teamRef {
	return s.ticks.TeamByNum(num)
}

// statfeedEnvelope mirrors the wire shape of a StatfeedEvent. RL ships
// either PascalCase or all-lowercase keys depending on build, so we
// accept both via the case-tolerant inner Data unwrap (envelopeData below).
type statfeedEnvelope struct {
	Data    string `json:"Data"`
	DataLow string `json:"data"`
}

type statfeedData struct {
	MatchGUID       string       `json:"MatchGuid"`
	MatchGUIDLow    string       `json:"matchguid"`
	EventName       string       `json:"EventName"`
	EventNameLow    string       `json:"eventname"`
	Type            string       `json:"Type"`
	TypeLow         string       `json:"type"`
	MainTarget      *ShortcutRef `json:"MainTarget"`
	MainTargetLow   *ShortcutRef `json:"maintarget"`
	SecondaryTarget *ShortcutRef `json:"SecondaryTarget"`
	SecondTargetLow *ShortcutRef `json:"secondarytarget"`
}


// unwrapInnerData pulls the inner Data string out of an envelope, accepting
// both PascalCase and lowercase top-level keys. Returns "" on missing or
// malformed envelope.
func unwrapInnerData(raw []byte) string {
	var env struct {
		Data    string `json:"Data"`
		DataLow string `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return ""
	}
	if env.Data != "" {
		return env.Data
	}
	return env.DataLow
}

// ballRef is the ball location/speed block found on BallHit and CrossbarHit.
type ballRef struct {
	PreHitSpeed  *float64 `json:"PreHitSpeed,omitempty"`
	PostHitSpeed *float64 `json:"PostHitSpeed,omitempty"`
	Location     *vec3    `json:"Location,omitempty"`
}

type vec3 struct {
	X float64 `json:"X"`
	Y float64 `json:"Y"`
	Z float64 `json:"Z"`
}

type ballHitData struct {
	MatchGUID    string         `json:"MatchGuid"`
	MatchGUIDLow string         `json:"matchguid"`
	Players      []ShortcutRef  `json:"Players"`
	PlayersLow   []ShortcutRef  `json:"players"`
	Ball         *ballHitInner  `json:"Ball"`
	BallLow      *ballHitInner  `json:"ball"`
}

type ballHitInner struct {
	PreHitSpeed  *float64 `json:"PreHitSpeed"`
	PostHitSpeed *float64 `json:"PostHitSpeed"`
	Location     *vec3    `json:"Location"`
}

type enrichedBallHit struct {
	Event        string            `json:"Event"`
	MatchGUID    string            `json:"matchGuid,omitempty"`
	Players      []*EnrichedPlayer `json:"players"`
	PreHitSpeed  *float64          `json:"preHitSpeed,omitempty"`
	PostHitSpeed *float64          `json:"postHitSpeed,omitempty"`
	Location     *vec3             `json:"location,omitempty"`
}

// crossbarHitData mirrors the wire shape of a CrossbarHit envelope.
// BallLastTouch is the player who last touched the ball before it hit
// the crossbar — typically the shooter.
type crossbarHitData struct {
	MatchGUID        string         `json:"MatchGuid"`
	MatchGUIDLow     string         `json:"matchguid"`
	BallSpeed        *float64       `json:"BallSpeed"`
	BallSpeedLow     *float64       `json:"ballspeed"`
	ImpactForce      *float64       `json:"ImpactForce"`
	ImpactForceLow   *float64       `json:"impactforce"`
	BallLocation     *vec3          `json:"BallLocation"`
	BallLocationLow  *vec3          `json:"balllocation"`
	BallLastTouch    *ballLastTouch `json:"BallLastTouch"`
	BallLastTouchLow *ballLastTouch `json:"balllasttouch"`
}

type ballLastTouch struct {
	Player    *ShortcutRef `json:"Player"`
	PlayerLow *ShortcutRef `json:"player"`
	Speed     *float64     `json:"Speed"`
	SpeedLow  *float64     `json:"speed"`
}

type enrichedBallLastTouch struct {
	Player *EnrichedPlayer `json:"player,omitempty"`
	Speed  *float64        `json:"speed,omitempty"`
}

func pickStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func pickFloat(a, b *float64) *float64 {
	if a != nil {
		return a
	}
	return b
}

// matchEndedData mirrors the wire shape of MatchEnded.
type matchEndedData struct {
	MatchGUID       string `json:"MatchGuid"`
	MatchGUIDLow    string `json:"matchguid"`
	WinnerTeamNum   *int   `json:"WinnerTeamNum"`
	WinnerTeamLow   *int   `json:"winnerteamnum"`
}

type enrichedMatchEnded struct {
	Event         string  `json:"Event"`
	MatchGUID     string  `json:"matchGuid,omitempty"`
	WinnerTeamNum *int    `json:"winnerTeamNum,omitempty"`
	WinnerName    string  `json:"winnerName,omitempty"`
	ScoreBlue     *int    `json:"scoreBlue,omitempty"`
	ScoreOrange   *int    `json:"scoreOrange,omitempty"`
}

func (s *Synthesizer) onMatchEnded(raw []byte) {
	inner := unwrapInnerData(raw)
	if inner == "" {
		return
	}
	var d matchEndedData
	if err := json.Unmarshal([]byte(inner), &d); err != nil {
		return
	}
	guid := pickStr(d.MatchGUID, d.MatchGUIDLow)
	winner := d.WinnerTeamNum
	if winner == nil {
		winner = d.WinnerTeamLow
	}

	out := enrichedMatchEnded{
		Event:         "_MatchEnded",
		MatchGUID:     guid,
		WinnerTeamNum: winner,
	}

	// Resolve final scores from the cached Teams[] so subscribers don't
	// have to track UpdateState themselves.
	if blue := s.teamByNum(0); blue != nil {
		score := blue.Score
		out.ScoreBlue = &score
	}
	if orange := s.teamByNum(1); orange != nil {
		score := orange.Score
		out.ScoreOrange = &score
	}
	if winner != nil {
		if t := s.teamByNum(*winner); t != nil {
			out.WinnerName = t.Name
		}
	}

	b, err := json.Marshal(out)
	if err != nil {
		return
	}
	s.bus.Broadcast(Event{Raw: b})
}

// removed: beginMatchSummary, armPodiumSettle, flushMatchSummary, MVP
// stitching in onStatfeedEvent. _MatchSummary and _MatchMVP are deleted
// in the new wire spec. Whatever stub follows below was the former
// settle-window machinery — now dead code that the next edit removes.

// goalScoredData mirrors the wire shape of GoalScored.
type goalScoredData struct {
	MatchGUID       string         `json:"MatchGuid"`
	MatchGUIDLow    string         `json:"matchguid"`
	Scorer          *ShortcutRef   `json:"Scorer"`
	ScorerLow       *ShortcutRef   `json:"scorer"`
	Assister        *ShortcutRef   `json:"Assister"`
	AssisterLow     *ShortcutRef   `json:"assister"`
	BallLastTouch   *ballLastTouch `json:"BallLastTouch"`
	BallLastTouchLow *ballLastTouch `json:"balllasttouch"`
	GoalSpeed       *float64       `json:"GoalSpeed"`
	GoalSpeedLow    *float64       `json:"goalspeed"`
	GoalTime        *float64       `json:"GoalTime"`
	GoalTimeLow     *float64       `json:"goaltime"`
	ImpactLocation  *vec3          `json:"ImpactLocation"`
	ImpactLocationLow *vec3        `json:"impactlocation"`
}

type enrichedGoalScored struct {
	Event           string                 `json:"Event"`
	MatchGUID       string                 `json:"matchGuid,omitempty"`
	Scorer          *EnrichedPlayer        `json:"scorer,omitempty"`
	Assister        *EnrichedPlayer        `json:"assister,omitempty"`
	BallLastTouch   *enrichedBallLastTouch `json:"ballLastTouch,omitempty"`
	GoalSpeed       *float64               `json:"goalSpeed,omitempty"`
	GoalTime        *float64               `json:"goalTime,omitempty"`
	ImpactLocation  *vec3                  `json:"impactLocation,omitempty"`
	ScoringTeam     *int                   `json:"scoringTeam,omitempty"`
	ConcedingTeam   *int                   `json:"concedingTeam,omitempty"`
	IsOwnGoal       bool                   `json:"isOwnGoal"`
	Modifiers       *goalModifiers         `json:"modifiers,omitempty"`
}

// goalModifiers carries the same-frame statfeed flags RL fires alongside
// a goal. Only flags that fire are populated; missing fields are absent
// rather than `false` so consumers can use truthy checks. Modifier
// detection is best-effort: RL's modifier statfeeds aren't fully
// documented and game-mode-specific flags (poolShot, hoopsSwishGoal)
// only exist in their respective modes.
type goalModifiers struct {
	IsAerialGoal       bool `json:"isAerialGoal,omitempty"`
	IsBackwardsGoal    bool `json:"isBackwardsGoal,omitempty"`
	IsBicycleGoal      bool `json:"isBicycleGoal,omitempty"`
	IsLongGoal         bool `json:"isLongGoal,omitempty"`
	IsTurtleGoal       bool `json:"isTurtleGoal,omitempty"`
	IsOvertimeGoal     bool `json:"isOvertimeGoal,omitempty"`
	IsPoolShot         bool `json:"isPoolShot,omitempty"`
	IsHoopsSwishGoal   bool `json:"isHoopsSwishGoal,omitempty"`
	IsHatTrickGoal     bool `json:"isHatTrickGoal,omitempty"`
	// IsFlipResetGoal is toolkit-detected, not from a Statfeed: scorer
	// got a FlipReset and stayed airborne (bOnGround=false) until
	// scoring. See flipResetArmed bookkeeping in onUpdateState +
	// emitFlipReset, consumed in onGoalScored.
	IsFlipResetGoal    bool `json:"isFlipResetGoal,omitempty"`
}

// modifierStatfeedNames maps the statfeed EventName (as RL ships it) to
// the goalModifiers boolean field that should be set. Only same-player
// matches against the scorer count.
var modifierStatfeedNames = map[string]func(*goalModifiers){
	"AerialGoal":     func(m *goalModifiers) { m.IsAerialGoal = true },
	"BackwardsGoal":  func(m *goalModifiers) { m.IsBackwardsGoal = true },
	"BicycleGoal":    func(m *goalModifiers) { m.IsBicycleGoal = true },
	"LongGoal":       func(m *goalModifiers) { m.IsLongGoal = true },
	"TurtleGoal":     func(m *goalModifiers) { m.IsTurtleGoal = true },
	"OvertimeGoal":   func(m *goalModifiers) { m.IsOvertimeGoal = true },
	"PoolShot":       func(m *goalModifiers) { m.IsPoolShot = true },
	"HoopsSwishGoal": func(m *goalModifiers) { m.IsHoopsSwishGoal = true },
	"HatTrick":       func(m *goalModifiers) { m.IsHatTrickGoal = true },
}

func (s *Synthesizer) onGoalScored(raw []byte) {
	inner := unwrapInnerData(raw)
	if inner == "" {
		return
	}
	var d goalScoredData
	if err := json.Unmarshal([]byte(inner), &d); err != nil {
		return
	}

	scorerRef := d.Scorer
	if scorerRef == nil {
		scorerRef = d.ScorerLow
	}
	// SDK has a guard: drop GoalScored events where the scorer can't be
	// identified. RL re-fires GoalScored at round-restart with empty
	// Scorer.Name; without this drop, a synthetic event with name
	// "Unknown" overwrites the prior tick's scorer for any plugin that
	// caches the latest goal. Mirror that guard here.
	if scorerRef == nil || scorerRef.Name == "" {
		return
	}
	scorer := s.roster.ResolveByShortcut(*scorerRef)
	if scorer == nil {
		return
	}

	guid := pickStr(d.MatchGUID, d.MatchGUIDLow)
	assisterRef := d.Assister
	if assisterRef == nil {
		assisterRef = d.AssisterLow
	}
	lastTouch := d.BallLastTouch
	if lastTouch == nil {
		lastTouch = d.BallLastTouchLow
	}
	loc := d.ImpactLocation
	if loc == nil {
		loc = d.ImpactLocationLow
	}

	out := enrichedGoalScored{
		Event:          "_GoalScored",
		MatchGUID:      guid,
		Scorer:         scorer,
		GoalSpeed:      pickFloat(d.GoalSpeed, d.GoalSpeedLow),
		GoalTime:       pickFloat(d.GoalTime, d.GoalTimeLow),
		ImpactLocation: loc,
	}

	// Scoring team is the scorer's team; conceding is the other.
	scoringTeam := scorer.Team
	concedingTeam := 1 - scoringTeam
	out.ScoringTeam = &scoringTeam
	out.ConcedingTeam = &concedingTeam

	if assisterRef != nil && assisterRef.Name != "" {
		out.Assister = s.roster.ResolveByShortcut(*assisterRef)
	}

	var lastToucher *EnrichedPlayer
	if lastTouch != nil {
		ref := lastTouch.Player
		if ref == nil {
			ref = lastTouch.PlayerLow
		}
		sp := pickFloat(lastTouch.Speed, lastTouch.SpeedLow)
		if ref != nil {
			lastToucher = s.roster.ResolveByShortcut(*ref)
		}
		if lastToucher != nil || sp != nil {
			out.BallLastTouch = &enrichedBallLastTouch{
				Player: lastToucher,
				Speed:  sp,
			}
		}
	}
	// Fallback: when RL ships GoalScored without a BallLastTouch block
	// (observed on some builds, especially for own goals), use the
	// most recent BallHit from the shared correlation buffer. Same
	// heuristic, just sourced from the producer that already records it.
	if lastToucher == nil {
		for _, p := range s.correlation.Recent("BallHit", 1) {
			if rec, ok := p.(*ballHitRecord); ok && rec != nil {
				lastToucher = rec.Player
			}
		}
	}
	// Own-goal heuristic: if the last-touch player is on the conceding
	// team, the goal was deflected/own-goaled. The richer _OwnGoal
	// event ships in Phase 2 with score-delta verification; this flag
	// is the cheap header.
	if lastToucher != nil && lastToucher.Team == concedingTeam {
		out.IsOwnGoal = true
	}

	// Bump the per-player real-goal counter for non-own-goals so
	// emitHatTrick can verify RL's HatTrick threshold against actual
	// scoring (RL's own counter includes own goals, which most
	// communities don't count toward a hat trick).
	if !out.IsOwnGoal && scorer.ID != "" {
		s.realGoalsMu.Lock()
		s.realGoalsByID[scorer.ID]++
		s.realGoalsMu.Unlock()
	}

	// Modifier flags via the correlation buffer. Statfeeds fire on the
	// same frame as the goal (or one before/after), so a 3-event window
	// is plenty. Match by Shortcut (RL's spectator-name identifier);
	// fall back to Name for safety.
	mods := s.collectGoalModifiers(scorerRef)

	// RL's HatTrick Statfeed counts own goals toward the threshold. If
	// our real-goal count is below 3 the player hasn't actually scored
	// a hat trick (e.g. 2 honest goals + 1 own goal = 3 by RL, 2 by us).
	// Clear the modifier flag so consumers see consistent behavior.
	if mods != nil && mods.IsHatTrickGoal && scorer.ID != "" {
		s.realGoalsMu.Lock()
		real := s.realGoalsByID[scorer.ID]
		s.realGoalsMu.Unlock()
		if real < 3 {
			mods.IsHatTrickGoal = false
		}
	}

	// Flip-reset goal: scorer was armed by a prior FlipReset and
	// hadn't touched ground since. Consume the flag via StatfeedEmitter
	// (which now owns it).
	if s.statfeed != nil && s.statfeed.ConsumeFlipResetArm(scorer.ID) {
		if mods == nil {
			mods = &goalModifiers{}
		}
		mods.IsFlipResetGoal = true
	}
	if mods != nil {
		out.Modifiers = mods
	}

	// Record the resolved goal into the correlation buffer so _OwnGoal
	// (Phase 2) can attach it as `correlatedGoal`. The Phase-3 _Assist
	// emitter will also read this entry.
	rec := &goalRecord{
		Scorer:        scorer,
		ScoringTeam:   scoringTeam,
		ConcedingTeam: concedingTeam,
	}
	s.correlation.Record("_GoalScored", rec)

	// Dedicated cache for _GoalReplayStarted. The correlation buffer is
	// shared with BallHit/StatfeedEvent and a goal celebration can evict
	// the _GoalScored entry before GoalReplayStart arrives. Cache the
	// full enriched envelope so _GoalReplayStarted can mirror every
	// field (assister, ballLastTouch, modifiers, …).
	cached := out
	s.lastGoalMu.Lock()
	s.lastGoal = &cached
	s.lastGoalMu.Unlock()

	b, err := json.Marshal(out)
	if err != nil {
		return
	}
	s.bus.Broadcast(Event{Raw: b})
}

// goalRecord is the slim correlation-buffer entry for _GoalScored —
// just the bits _OwnGoal / _Assist need to relate themselves to a goal.
type goalRecord struct {
	Scorer        *EnrichedPlayer
	ScoringTeam   int
	ConcedingTeam int
}

// collectGoalModifiers walks the correlation buffer for same-player
// modifier statfeeds. Returns nil when nothing matches so the encoded
// JSON omits the modifiers field entirely.
func (s *Synthesizer) collectGoalModifiers(scorer *ShortcutRef) *goalModifiers {
	if scorer == nil {
		return nil
	}
	var mods *goalModifiers

	apply := func(name string) {
		setter, ok := modifierStatfeedNames[name]
		if !ok {
			return
		}
		if mods == nil {
			mods = &goalModifiers{}
		}
		setter(mods)
	}

	// FindWithin runs the predicate against every StatfeedEvent in the
	// last 15-event window and returns the first match. We need to
	// inspect ALL same-player statfeeds, not just one — so use Recent.
	for _, p := range s.correlation.Recent("StatfeedEvent", 15) {
		rec, ok := p.(*statfeedRecord)
		if !ok {
			continue
		}
		if rec.MainRef == nil {
			continue
		}
		if !sameShortcutPlayer(rec.MainRef, scorer) {
			continue
		}
		apply(rec.EventName)
	}

	return mods
}

// sameShortcutPlayer compares two shortcut refs for "same player". RL's
// Shortcut is the per-match slot index (number); Name is the
// human-readable form. Either match counts; both unset means no match.
func sameShortcutPlayer(a, b *ShortcutRef) bool {
	if a == nil || b == nil {
		return false
	}
	if a.Shortcut != 0 && b.Shortcut != 0 && a.Shortcut == b.Shortcut {
		return true
	}
	if a.Name != "" && b.Name != "" && a.Name == b.Name {
		return true
	}
	return false
}
