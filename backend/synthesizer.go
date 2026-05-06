package main

import (
	"encoding/json"
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
	// synth's tick-diff path calls ClearFlipResetArm when a player
	// touches ground.
	statfeed *StatfeedEmitter

	// correlation is the shared sliding window of recent events.
	// Owned by main; the synth no longer writes to it but a couple
	// of remaining read sites (currently none, kept for the diff
	// emitters that may move here in 5.10) still hold the handle.
	correlation *CorrelationBuffer
}

func NewSynthesizer(bus Broadcaster, roster *RosterTracker, correlation *CorrelationBuffer, ticks *TickStore) *Synthesizer {
	return &Synthesizer{
		bus:         bus,
		roster:      roster,
		correlation: correlation,
		ticks:       ticks,
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

// AttachStatfeedEmitter wires the StatfeedEmitter so the synth's
// tick-diff path can call ClearFlipResetArm on a player's
// ground-touch.
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
	case "MatchEnded":
		s.onMatchEnded(raw)
	}
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

	// _GoalReplayStarted has moved to GoalEmitter (which owns the
	// lastGoal cache); no replay-edge detection here anymore.

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
