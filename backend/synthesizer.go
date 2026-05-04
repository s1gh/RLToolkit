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
	bus    *EventBus
	roster *RosterTracker
	// phaseMachine is consulted by emitters that should only fire during
	// real gameplay (e.g. _OwnGoal — a score-delta during a forfeit or
	// mercy rule must not flag as an own goal). Optional; nil disables
	// the gate, which is what the unit tests do.
	phaseMachine *PhaseMachine

	// teamsMu guards lastTeams + lastTick. Cached from the most recent
	// UpdateState so _MatchEnded can resolve WinnerTeamNum to a name
	// and Phase-4 diff emitters can compare current vs previous tick.
	teamsMu   sync.Mutex
	lastTeams []teamRef
	lastTick  *tickSnapshot

	// correlation buffers same-frame and adjacent-tick events so emitters
	// like _GoalScored can look back for modifier Statfeeds (AerialGoal,
	// BackwardsGoal, …) and _OwnGoal can look back for the GoalScored it
	// belongs to. Capacity 15 ≈ 3 ticks at 60 Hz / ~5 events per tick.
	correlation *CorrelationBuffer

	// lastBallTouchMu guards lastBallTouchPlayer/Team. Updated from each
	// BallHit envelope so _OwnGoal can identify the player who deflected
	// the ball into their own net (the "deflector"). The plan requires
	// this for Phase 2's score-delta own-goal detection.
	lastBallTouchMu     sync.Mutex
	lastBallTouchPlayer *EnrichedPlayer

	// matchMu guards per-match milestone state. Reset on MatchCreated
	// / MatchDestroyed so a back-to-back match doesn't carry stale
	// flags. _OvertimeStarted / _FirstTouch / _FirstBlood each fire
	// at most once per match.
	matchMu              sync.Mutex
	awaitingFirstTouch   bool // true after RoundStarted, until the first BallHit
	firstBloodFired      bool
	overtimeStartedFired bool
	matchInitializedAt   time.Time // for _FirstBlood.secondsIntoMatch
	roundStartedAt       time.Time // for _FirstTouch.timeFromCountdownEndSeconds

	// Match summary settle-window state. Set on MatchEnded; cleared
	// when _MatchSummary publishes (settle timeout, PodiumStart, or
	// MatchDestroyed — whichever first).
	summaryMu       sync.Mutex
	summaryPending  bool
	summaryGUID     string
	summaryWinner   *int
	summaryFinalSnap *tickSnapshot
	summaryMVP      *EnrichedPlayer
	summaryCancel   chan struct{}
}

// matchSummarySettleWindow is how long after MatchEnded we wait for
// the late MVP Statfeed to arrive. The plan calls this "~2s"; we set
// 2s exactly. PodiumStart or MatchDestroyed short-circuit the wait.
const matchSummarySettleWindow = 2 * time.Second

func NewSynthesizer(bus *EventBus, roster *RosterTracker) *Synthesizer {
	return &Synthesizer{
		bus:         bus,
		roster:      roster,
		correlation: NewCorrelationBuffer(15),
	}
}

// AttachPhaseMachine wires the gameplay-phase tracker so emitters that
// should only fire during real gameplay can gate on it. Call before Run.
func (s *Synthesizer) AttachPhaseMachine(m *PhaseMachine) { s.phaseMachine = m }

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
	case "StatfeedEvent":
		s.onStatfeedEvent(raw)
	case "BallHit":
		s.onBallHit(raw)
	case "CrossbarHit":
		s.onCrossbarHit(raw)
	case "MatchEnded":
		s.onMatchEnded(raw)
	case "GoalScored":
		s.onGoalScored(raw)
	case "MatchCreated":
		s.resetMatchMilestones()
	case "MatchDestroyed":
		// MatchDestroyed short-circuits the summary settle window so a
		// fast-quitting user still gets a _MatchSummary (with whatever
		// fields were collected). Also reset the per-match flags.
		s.flushMatchSummary("MatchDestroyed")
		s.resetMatchMilestones()
	case "MatchInitialized":
		s.onMatchInitialized()
	case "RoundStarted":
		s.armFirstTouch()
	case "GoalReplayStart":
		s.onGoalReplayStart(raw)
	case "PodiumStart":
		// PodiumStart short-circuits the summary settle window — at
		// this point RL has frozen scores and the MVP statfeed has
		// arrived (or never will).
		s.flushMatchSummary("PodiumStart")
	}
}

// onGoalReplayStart emits _GoalReplayContext with the most recently
// cached _GoalScored, so plugins know which goal the replay is for
// without having to correlate the events themselves.
func (s *Synthesizer) onGoalReplayStart(raw []byte) {
	var record *goalRecord
	for _, p := range s.correlation.Recent("_GoalScored", 5) {
		if g, ok := p.(*goalRecord); ok {
			record = g
			break
		}
	}
	if record == nil {
		// No cached goal — nothing useful to ship. The raw
		// GoalReplayStart still flows to subscribers; we just don't
		// add the context envelope.
		return
	}
	guid := pickStr("", "")
	// Pull guid from the envelope so subscribers can correlate against
	// MatchGuid. Cheap full-decode given the event is rare.
	inner := unwrapInnerData(raw)
	if inner != "" {
		var d struct {
			MatchGUID    string `json:"MatchGuid"`
			MatchGUIDLow string `json:"matchguid"`
		}
		if err := json.Unmarshal([]byte(inner), &d); err == nil {
			guid = pickStr(d.MatchGUID, d.MatchGUIDLow)
		}
	}
	out := struct {
		Event         string          `json:"Event"`
		MatchGUID     string          `json:"matchGuid,omitempty"`
		Scorer        *EnrichedPlayer `json:"scorer,omitempty"`
		ScoringTeam   int             `json:"scoringTeam"`
		ConcedingTeam int             `json:"concedingTeam"`
	}{
		Event:         "_GoalReplayContext",
		MatchGUID:     guid,
		Scorer:        record.Scorer,
		ScoringTeam:   record.ScoringTeam,
		ConcedingTeam: record.ConcedingTeam,
	}
	if b, err := json.Marshal(out); err == nil {
		s.bus.Publish(b)
	}
}

// resetMatchMilestones clears every per-match flag. Called on
// MatchCreated (new match starting) and MatchDestroyed (back to menu).
// Without this, a back-to-back rematch into the same lobby would
// silently skip _FirstBlood because firstBloodFired was still true.
func (s *Synthesizer) resetMatchMilestones() {
	s.matchMu.Lock()
	s.awaitingFirstTouch = false
	s.firstBloodFired = false
	s.overtimeStartedFired = false
	s.matchInitializedAt = time.Time{}
	s.matchMu.Unlock()
}

func (s *Synthesizer) onMatchInitialized() {
	s.matchMu.Lock()
	s.matchInitializedAt = time.Now()
	s.matchMu.Unlock()
}

func (s *Synthesizer) armFirstTouch() {
	// _FirstTouch fires once per kickoff (every RoundStarted), not once
	// per match. Each goal ends with a kickoff, so this re-arms naturally.
	s.matchMu.Lock()
	s.awaitingFirstTouch = true
	s.roundStartedAt = time.Now()
	s.matchMu.Unlock()
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
}

type updateStateFullPlayer struct {
	PrimaryID    string `json:"PrimaryId"`
	Name         string `json:"Name"`
	TeamNum      int    `json:"TeamNum"`
	Score        int    `json:"Score"`
	Goals        int    `json:"Goals"`
	Assists      int    `json:"Assists"`
	Saves        int    `json:"Saves"`
	Shots        int    `json:"Shots"`
	Touches      int    `json:"Touches"`
	CarTouches   int    `json:"CarTouches"`
	Demos        int    `json:"Demos"`
	Boost        *int   `json:"Boost"`
	BDemolished  bool   `json:"bDemolished"`
}

// onUpdateState caches the latest UpdateState slice for downstream
// enrichment and runs Phase-4 diff emitters: _PlayerJoined / _PlayerLeft,
// _PlayerScoreChanged, _BoostPickup, _BallPossessionChanged,
// _TeamScoreChanged, plus _OwnGoal (which predates Phase 4 but lives
// here for ordering with _TeamScoreChanged). Stays cheap — most of
// the work is sub-millisecond integer compares.
func (s *Synthesizer) onUpdateState(raw []byte) {
	inner := unwrapInnerData(raw)
	if inner == "" {
		return
	}
	var d updateStateFull
	if err := json.Unmarshal([]byte(inner), &d); err != nil {
		return
	}
	g := d.Game
	if g == nil {
		g = d.GameLow
	}
	if g == nil || len(g.Teams) == 0 {
		return
	}

	guid := pickStr(d.MatchGUID, d.MatchGUIDLow)
	teams := make([]teamRef, 0, len(g.Teams))
	for _, t := range g.Teams {
		teams = append(teams, teamRef{
			TeamNum:        t.TeamNum,
			Name:           t.Name,
			Score:          t.Score,
			ColorPrimary:   t.ColorPrimary,
			ColorSecondary: t.ColorSecondary,
		})
	}

	wirePlayers := d.Players
	if len(wirePlayers) == 0 {
		wirePlayers = d.PlayersLow
	}
	players := make([]tickPlayer, 0, len(wirePlayers))
	for _, p := range wirePlayers {
		var boost *int
		if p.Boost != nil {
			b := *p.Boost
			boost = &b
		}
		players = append(players, tickPlayer{
			id:         p.PrimaryID,
			name:       p.Name,
			team:       p.TeamNum,
			score:      p.Score,
			goals:      p.Goals,
			assists:    p.Assists,
			saves:      p.Saves,
			shots:      p.Shots,
			touches:    p.Touches,
			carTouches: p.CarTouches,
			demos:      p.Demos,
			boost:      boost,
			demolished: p.BDemolished,
		})
	}

	curr := &tickSnapshot{
		matchGUID: guid,
		teams:     teams,
		players:   players,
		overtime:  g.BOvertime,
	}
	if g.Ball != nil {
		curr.ballTeam = g.Ball.TeamNum
		curr.hasBall = true
	}

	s.teamsMu.Lock()
	prev := s.lastTick
	prevTeams := s.lastTeams
	s.lastTick = curr
	s.lastTeams = teams
	s.teamsMu.Unlock()

	// _OwnGoal predates Phase 4 but is also a per-team-score-delta
	// emitter, so we keep it here. Skip the compare on the first tick
	// (no baseline).
	if len(prevTeams) > 0 {
		s.detectOwnGoal(prevTeams, teams, guid)
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

	s.diffPlayers(prev, curr)
	s.diffTeamScores(prev, curr)
	s.diffBallPossession(prev, curr)
	s.diffOvertime(prev, curr)
}

// diffOvertime fires _OvertimeStarted on a rising bOvertime edge.
// Once-per-match: subsequent OT ticks don't re-fire.
func (s *Synthesizer) diffOvertime(prev, curr *tickSnapshot) {
	if prev.overtime || !curr.overtime {
		return
	}
	s.matchMu.Lock()
	if s.overtimeStartedFired {
		s.matchMu.Unlock()
		return
	}
	s.overtimeStartedFired = true
	matchStart := s.matchInitializedAt
	s.matchMu.Unlock()

	var matchDur *float64
	if !matchStart.IsZero() {
		dur := time.Since(matchStart).Seconds()
		matchDur = &dur
	}
	out := struct {
		Event                          string  `json:"Event"`
		MatchGUID                      string  `json:"matchGuid,omitempty"`
		ScoreBlue                      int     `json:"scoreBlue"`
		ScoreOrange                    int     `json:"scoreOrange"`
		MatchDurationSecondsBeforeOT   *float64 `json:"matchDurationSecondsBeforeOT,omitempty"`
	}{
		Event:                        "_OvertimeStarted",
		MatchGUID:                    curr.matchGUID,
		ScoreBlue:                    teamScore(curr.teams, 0),
		ScoreOrange:                  teamScore(curr.teams, 1),
		MatchDurationSecondsBeforeOT: matchDur,
	}
	if b, err := json.Marshal(out); err == nil {
		s.bus.Publish(b)
	}
}

// diffPlayers walks the previous + current player lists and emits
// _PlayerJoined / _PlayerLeft on roster identity changes,
// _PlayerScoreChanged on stat-field deltas, and _BoostPickup on a
// rising boost edge during live play.
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

	// _PlayerScoreChanged + _BoostPickup: walk the intersection.
	for id, c := range currByID {
		p, ok := prevByID[id]
		if !ok {
			continue
		}
		s.emitPlayerScoreChangedIfDelta(curr.matchGUID, p, c)
		s.emitBoostPickupIfRising(curr.matchGUID, p, c)
	}
}

// emitPlayerJoined publishes a _PlayerJoined envelope. Phase is left
// to the SDK to attach (it has the lifecycle subscription). We carry
// just the identity bits.
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
	}{
		Event:     "_PlayerJoined",
		MatchGUID: guid,
		Player:    enriched,
	}
	if b, err := json.Marshal(out); err == nil {
		s.bus.Publish(b)
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
	}{
		Event:     "_PlayerLeft",
		MatchGUID: guid,
		Player:    enriched,
	}
	if b, err := json.Marshal(out); err == nil {
		s.bus.Publish(b)
	}
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
		s.bus.Publish(b)
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
		s.bus.Publish(b)
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
			s.bus.Publish(b)
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
		Event     string `json:"Event"`
		MatchGUID string `json:"matchGuid,omitempty"`
		Before    *int   `json:"before"`
		After     *int   `json:"after"`
	}{
		Event:     "_BallPossessionChanged",
		MatchGUID: curr.matchGUID,
		Before:    toNullable(prev.ballTeam),
		After:     toNullable(curr.ballTeam),
	}
	if b, err := json.Marshal(out); err == nil {
		s.bus.Publish(b)
	}
}

// detectOwnGoal compares two Teams[] snapshots and emits _OwnGoal when
// a team's score went up by 1 and the most recent ball touch was by an
// opposing-team player. Gated on the phase machine when attached so a
// forfeit/mercy-rule score-up doesn't false-positive.
func (s *Synthesizer) detectOwnGoal(prev, curr []teamRef, guid string) {
	// Build a quick lookup from TeamNum → previous score.
	prevByNum := make(map[int]int, len(prev))
	for _, p := range prev {
		prevByNum[p.TeamNum] = p.Score
	}

	for _, t := range curr {
		oldScore, ok := prevByNum[t.TeamNum]
		if !ok {
			continue
		}
		delta := t.Score - oldScore
		if delta != 1 {
			continue
		}
		// Phase gate: only fire during real gameplay. PhaseLive/Replay
		// covers active play; PhaseLobby/None/Countdown/Podium do not.
		if s.phaseMachine != nil {
			ph := s.phaseMachine.Current()
			if ph != PhaseNameLive && ph != PhaseNameReplay {
				continue
			}
		}

		// Find the most recent ball touch (within 5 events, ~1 tick).
		var touchPlayer *EnrichedPlayer
		for _, p := range s.correlation.Recent("BallHit", 5) {
			if pl, ok := p.(*EnrichedPlayer); ok && pl != nil {
				touchPlayer = pl
				break
			}
		}
		if touchPlayer == nil {
			continue
		}
		// Own-goal signature: deflector's team is NOT the team that
		// scored.
		if touchPlayer.Team == t.TeamNum {
			continue
		}

		// Optional: attach a recently-emitted _GoalScored if one fired
		// in the same window — gives subscribers the full goal payload
		// alongside the detection.
		var correlatedGoal *goalRecord
		for _, p := range s.correlation.Recent("_GoalScored", 5) {
			if g, ok := p.(*goalRecord); ok {
				correlatedGoal = g
				break
			}
		}

		out := enrichedOwnGoal{
			Event:         "_OwnGoal",
			MatchGUID:     guid,
			Deflector:     touchPlayer,
			ScoringTeam:   t.TeamNum,
			ConcedingTeam: touchPlayer.Team,
			ScoreAfter: ownGoalScoreAfter{
				Blue:   teamScore(curr, 0),
				Orange: teamScore(curr, 1),
			},
		}
		if correlatedGoal != nil && correlatedGoal.Scorer != nil {
			out.CorrelatedGoalScorer = correlatedGoal.Scorer
		}
		b, err := json.Marshal(out)
		if err != nil {
			continue
		}
		s.bus.Publish(b)
	}
}

type enrichedOwnGoal struct {
	Event                string            `json:"Event"`
	MatchGUID            string            `json:"matchGuid,omitempty"`
	Deflector            *EnrichedPlayer   `json:"deflector,omitempty"`
	ScoringTeam          int               `json:"scoringTeam"`
	ConcedingTeam        int               `json:"concedingTeam"`
	ScoreAfter           ownGoalScoreAfter `json:"scoreAfter"`
	CorrelatedGoalScorer *EnrichedPlayer   `json:"correlatedGoalScorer,omitempty"`
}

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

// teamByNum returns a copy of the cached team with the given TeamNum,
// or nil if no UpdateState has populated the cache or the team isn't
// present.
func (s *Synthesizer) teamByNum(num int) *teamRef {
	s.teamsMu.Lock()
	defer s.teamsMu.Unlock()
	for i := range s.lastTeams {
		if s.lastTeams[i].TeamNum == num {
			t := s.lastTeams[i]
			return &t
		}
	}
	return nil
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

// enrichedStatfeed is the payload shape of _StatfeedEvent. Field names
// mirror the JS-side typed event so SDK consumers can switch without
// reshaping the data.
type enrichedStatfeed struct {
	Event           string          `json:"Event"`
	MatchGUID       string          `json:"matchGuid,omitempty"`
	EventName       string          `json:"eventName"`
	Type            string          `json:"type,omitempty"`
	MainTarget      *EnrichedPlayer `json:"mainTarget,omitempty"`
	SecondaryTarget *EnrichedPlayer `json:"secondaryTarget,omitempty"`
}

func (s *Synthesizer) onStatfeedEvent(raw []byte) {
	var env statfeedEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return
	}
	inner := env.Data
	if inner == "" {
		inner = env.DataLow
	}
	if inner == "" {
		return
	}
	var d statfeedData
	if err := json.Unmarshal([]byte(inner), &d); err != nil {
		return
	}

	guid := d.MatchGUID
	if guid == "" {
		guid = d.MatchGUIDLow
	}
	eventName := d.EventName
	if eventName == "" {
		eventName = d.EventNameLow
	}
	typeStr := d.Type
	if typeStr == "" {
		typeStr = d.TypeLow
	}
	main := d.MainTarget
	if main == nil {
		main = d.MainTargetLow
	}
	secondary := d.SecondaryTarget
	if secondary == nil {
		secondary = d.SecondTargetLow
	}

	out := enrichedStatfeed{
		Event:     "_StatfeedEvent",
		MatchGUID: guid,
		EventName: eventName,
		Type:      typeStr,
	}
	if main != nil {
		out.MainTarget = s.roster.ResolveByShortcut(*main)
	}
	if secondary != nil {
		out.SecondaryTarget = s.roster.ResolveByShortcut(*secondary)
	}

	// Record into the correlation buffer so _GoalScored / Phase-3 events
	// can look back for same-frame modifiers. Stash the wire ref for
	// shortcut-matching plus the resolved enrichment for forward use.
	s.correlation.Record("StatfeedEvent", &statfeedRecord{
		EventName: eventName,
		MainRef:   main,
		Resolved:  out.MainTarget,
	})

	b, err := json.Marshal(out)
	if err != nil {
		return
	}
	s.bus.Publish(b)

	// Phase-3 dedicated events. Each known Statfeed variant gets its
	// own _-prefixed envelope with variant-specific fields (correlations,
	// counts) on top of the resolved targets. The generic _StatfeedEvent
	// above keeps firing as the catch-all.
	s.emitStatfeedVariant(eventName, guid, out.MainTarget, out.SecondaryTarget)

	// MVP statfeed lands inside the _MatchSummary settle window in most
	// builds. Capture it so flushMatchSummary can attach it. We don't
	// promote MVP to its own _-prefixed event — the plan's Phase 3.3
	// explicitly leaves MVP / Playmaker / Savior on the catch-all only.
	if eventName == "MVP" && out.MainTarget != nil {
		s.summaryMu.Lock()
		if s.summaryPending {
			s.summaryMVP = out.MainTarget
		}
		s.summaryMu.Unlock()
	}
}

// emitStatfeedVariant fans the Statfeed variant out to its dedicated
// _-prefixed event. Untracked variants (Goal, Win, MVP, Playmaker,
// Savior, LowFive, HighFive — see the plan's Phase 3.3) are silently
// skipped; the generic _StatfeedEvent already covered them.
func (s *Synthesizer) emitStatfeedVariant(eventName, guid string, main, secondary *EnrichedPlayer) {
	switch eventName {
	case "Demolish":
		s.emitPlayerDemolished(guid, main, secondary)
	case "FlipReset":
		s.emitFlipReset(guid, main)
	case "HatTrick":
		s.emitHatTrick(guid, main)
	case "Save":
		s.emitSave(guid, main, "_Save")
	case "EpicSave":
		s.emitSave(guid, main, "_EpicSave")
	case "Shot":
		s.emitShot(guid, main)
	case "Assist":
		s.emitAssist(guid, main)
	case "Center":
		s.emitTouchVariant(guid, main, "_Center")
	case "Clear":
		s.emitTouchVariant(guid, main, "_Clear")
	case "BicycleHit":
		s.emitTouchVariant(guid, main, "_BicycleHit")
	}
}

// emitSimple publishes a minimal envelope with just the resolved
// MainTarget. Used by stat events with no correlation logic.
func (s *Synthesizer) emitSimple(eventName, guid string, main *EnrichedPlayer) {
	if main == nil {
		return
	}
	b, err := json.Marshal(struct {
		Event      string          `json:"Event"`
		MatchGUID  string          `json:"matchGuid,omitempty"`
		MainTarget *EnrichedPlayer `json:"mainTarget"`
	}{
		Event:      eventName,
		MatchGUID:  guid,
		MainTarget: main,
	})
	if err != nil {
		return
	}
	s.bus.Publish(b)
}

func (s *Synthesizer) emitFlipReset(guid string, main *EnrichedPlayer) {
	s.emitSimple("_FlipReset", guid, main)
}

// _PlayerDemolished carries both attacker (MainTarget) and victim
// (SecondaryTarget). isSelfDemo / isTeamDemo derived from team comparison.
func (s *Synthesizer) emitPlayerDemolished(guid string, attacker, victim *EnrichedPlayer) {
	if attacker == nil || victim == nil {
		// Without one of the two targets the event is meaningless;
		// the catch-all _StatfeedEvent already covered it.
		return
	}
	out := struct {
		Event      string          `json:"Event"`
		MatchGUID  string          `json:"matchGuid,omitempty"`
		Attacker   *EnrichedPlayer `json:"attacker"`
		Victim     *EnrichedPlayer `json:"victim"`
		IsSelfDemo bool            `json:"isSelfDemo,omitempty"`
		IsTeamDemo bool            `json:"isTeamDemo,omitempty"`
	}{
		Event:     "_PlayerDemolished",
		MatchGUID: guid,
		Attacker:  attacker,
		Victim:    victim,
	}
	if attacker.ID != "" && attacker.ID == victim.ID {
		out.IsSelfDemo = true
	} else if attacker.Team == victim.Team {
		out.IsTeamDemo = true
	}
	b, err := json.Marshal(out)
	if err != nil {
		return
	}
	s.bus.Publish(b)
}

// _HatTrick fires when the scorer's Goals count hits 3. We expose the
// goal count directly so subscribers don't need to look at UpdateState.
// The number comes from the cached match state — see syncedGoalsFor.
func (s *Synthesizer) emitHatTrick(guid string, main *EnrichedPlayer) {
	if main == nil {
		return
	}
	out := struct {
		Event           string          `json:"Event"`
		MatchGUID       string          `json:"matchGuid,omitempty"`
		MainTarget      *EnrichedPlayer `json:"mainTarget"`
		GoalsThisMatch  int             `json:"goalsThisMatch"`
	}{
		Event:          "_HatTrick",
		MatchGUID:      guid,
		MainTarget:     main,
		GoalsThisMatch: s.syncedGoalsFor(main.ID),
	}
	b, err := json.Marshal(out)
	if err != nil {
		return
	}
	s.bus.Publish(b)
}

// syncedGoalsFor reads the cached per-player Goals count from the
// most recent UpdateState. Returns 0 when the cache hasn't been
// populated or the player isn't in it. Per-player cache grows in
// Phase 4; for now there's no cache so this returns 0 — the plan
// explicitly notes goalsThisMatch is best-effort until Phase 4 lands.
func (s *Synthesizer) syncedGoalsFor(playerID string) int {
	_ = playerID
	return 0
}

// _Save / _EpicSave share their shape; differ only in event name. The
// plan's correlatedShot lookup ships in a follow-up — for now we expose
// the resolved saver and let consumers correlate on their own.
func (s *Synthesizer) emitSave(guid string, main *EnrichedPlayer, eventName string) {
	if main == nil {
		return
	}
	// Look back for a recent Shot statfeed by an opposing-team player.
	// "Recent" here is the full 15-event window — saves often follow a
	// shot by 5-30 ticks, which exceeds the modifier window.
	var correlatedShot *EnrichedPlayer
	for _, p := range s.correlation.Recent("StatfeedEvent", 15) {
		rec, ok := p.(*statfeedRecord)
		if !ok {
			continue
		}
		if rec.EventName != "Shot" {
			continue
		}
		if rec.Resolved == nil || rec.Resolved.Team == main.Team {
			continue
		}
		correlatedShot = rec.Resolved
		break
	}

	out := struct {
		Event          string          `json:"Event"`
		MatchGUID      string          `json:"matchGuid,omitempty"`
		MainTarget     *EnrichedPlayer `json:"mainTarget"`
		CorrelatedShot *EnrichedPlayer `json:"correlatedShot,omitempty"`
	}{
		Event:          eventName,
		MatchGUID:      guid,
		MainTarget:     main,
		CorrelatedShot: correlatedShot,
	}
	b, err := json.Marshal(out)
	if err != nil {
		return
	}
	s.bus.Publish(b)
}

// _Shot. We attempt to attach the same-frame BallHit (the touch that
// produced the shot) when one is in the buffer.
func (s *Synthesizer) emitShot(guid string, main *EnrichedPlayer) {
	if main == nil {
		return
	}
	var correlatedTouch *EnrichedPlayer
	for _, p := range s.correlation.Recent("BallHit", 3) {
		if pl, ok := p.(*EnrichedPlayer); ok && pl != nil {
			correlatedTouch = pl
			break
		}
	}
	out := struct {
		Event           string          `json:"Event"`
		MatchGUID       string          `json:"matchGuid,omitempty"`
		MainTarget      *EnrichedPlayer `json:"mainTarget"`
		CorrelatedTouch *EnrichedPlayer `json:"correlatedTouch,omitempty"`
	}{
		Event:           "_Shot",
		MatchGUID:       guid,
		MainTarget:      main,
		CorrelatedTouch: correlatedTouch,
	}
	b, err := json.Marshal(out)
	if err != nil {
		return
	}
	s.bus.Publish(b)
}

// _Assist. Look back for a same-frame _GoalScored — assists land on
// the same tick as the goal in most builds, but the plan calls this
// provisional because the window may need adjustment. We use 5 events.
func (s *Synthesizer) emitAssist(guid string, main *EnrichedPlayer) {
	if main == nil {
		return
	}
	var correlatedGoal *goalRecord
	for _, p := range s.correlation.Recent("_GoalScored", 5) {
		if g, ok := p.(*goalRecord); ok {
			correlatedGoal = g
			break
		}
	}
	type goalRef struct {
		Scorer        *EnrichedPlayer `json:"scorer,omitempty"`
		ScoringTeam   int             `json:"scoringTeam"`
		ConcedingTeam int             `json:"concedingTeam"`
	}
	var goalSummary *goalRef
	if correlatedGoal != nil {
		goalSummary = &goalRef{
			Scorer:        correlatedGoal.Scorer,
			ScoringTeam:   correlatedGoal.ScoringTeam,
			ConcedingTeam: correlatedGoal.ConcedingTeam,
		}
	}
	out := struct {
		Event          string          `json:"Event"`
		MatchGUID      string          `json:"matchGuid,omitempty"`
		MainTarget     *EnrichedPlayer `json:"mainTarget"`
		CorrelatedGoal *goalRef        `json:"correlatedGoal,omitempty"`
	}{
		Event:          "_Assist",
		MatchGUID:      guid,
		MainTarget:     main,
		CorrelatedGoal: goalSummary,
	}
	b, err := json.Marshal(out)
	if err != nil {
		return
	}
	s.bus.Publish(b)
}

// emitTouchVariant covers _Center, _Clear, _BicycleHit — same shape:
// resolved MainTarget + a correlatedTouch pulled from the BallHit
// buffer. Variant-specific fields (e.g. _BicycleHit.resultedInGoal
// forward correlation) ship in a follow-up.
func (s *Synthesizer) emitTouchVariant(guid string, main *EnrichedPlayer, eventName string) {
	if main == nil {
		return
	}
	var correlatedTouch *EnrichedPlayer
	for _, p := range s.correlation.Recent("BallHit", 3) {
		if pl, ok := p.(*EnrichedPlayer); ok && pl != nil {
			correlatedTouch = pl
			break
		}
	}
	out := struct {
		Event           string          `json:"Event"`
		MatchGUID       string          `json:"matchGuid,omitempty"`
		MainTarget      *EnrichedPlayer `json:"mainTarget"`
		CorrelatedTouch *EnrichedPlayer `json:"correlatedTouch,omitempty"`
	}{
		Event:           eventName,
		MatchGUID:       guid,
		MainTarget:      main,
		CorrelatedTouch: correlatedTouch,
	}
	b, err := json.Marshal(out)
	if err != nil {
		return
	}
	s.bus.Publish(b)
}

// statfeedRecord is what the correlation buffer holds for each
// StatfeedEvent. Only the fields _GoalScored / Phase-3 emitters look at
// are kept — small footprint per entry.
type statfeedRecord struct {
	EventName string
	MainRef   *ShortcutRef
	Resolved  *EnrichedPlayer
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

func (s *Synthesizer) onBallHit(raw []byte) {
	inner := unwrapInnerData(raw)
	if inner == "" {
		return
	}
	var d ballHitData
	if err := json.Unmarshal([]byte(inner), &d); err != nil {
		return
	}

	guid := d.MatchGUID
	if guid == "" {
		guid = d.MatchGUIDLow
	}
	players := d.Players
	if len(players) == 0 {
		players = d.PlayersLow
	}
	ball := d.Ball
	if ball == nil {
		ball = d.BallLow
	}

	resolved := make([]*EnrichedPlayer, 0, len(players))
	for _, p := range players {
		resolved = append(resolved, s.roster.ResolveByShortcut(p))
	}

	out := enrichedBallHit{
		Event:     "_BallHit",
		MatchGUID: guid,
		Players:   resolved,
	}
	if ball != nil {
		out.PreHitSpeed = ball.PreHitSpeed
		out.PostHitSpeed = ball.PostHitSpeed
		out.Location = ball.Location
	}

	// Track the most recent ball-touch player so _OwnGoal can identify
	// the deflector when a score-delta points at the wrong team. The
	// first player in BallHit.Players is the one who actually touched
	// the ball; multi-player BallHit events list secondary contacts
	// after, but RL's primary-touch convention puts [0] first.
	if len(resolved) > 0 && resolved[0] != nil {
		s.lastBallTouchMu.Lock()
		s.lastBallTouchPlayer = resolved[0]
		s.lastBallTouchMu.Unlock()
	}

	// Record the BallHit so _OwnGoal can correlate (Phase 2). Holding
	// the resolved first-toucher is enough for that emitter.
	if len(resolved) > 0 {
		s.correlation.Record("BallHit", resolved[0])
	}

	b, err := json.Marshal(out)
	if err != nil {
		return
	}
	s.bus.Publish(b)

	// _FirstTouch — fires on the first BallHit after each RoundStarted.
	s.maybeEmitFirstTouch(guid, resolved, out.PostHitSpeed, out.Location)
}

func (s *Synthesizer) maybeEmitFirstTouch(guid string, players []*EnrichedPlayer, postHitSpeed *float64, loc *vec3) {
	s.matchMu.Lock()
	if !s.awaitingFirstTouch {
		s.matchMu.Unlock()
		return
	}
	s.awaitingFirstTouch = false
	roundStart := s.roundStartedAt
	s.matchMu.Unlock()

	var elapsed *float64
	if !roundStart.IsZero() {
		dur := time.Since(roundStart).Seconds()
		elapsed = &dur
	}
	out := struct {
		Event                       string            `json:"Event"`
		MatchGUID                   string            `json:"matchGuid,omitempty"`
		Players                     []*EnrichedPlayer `json:"players"`
		PostHitSpeed                *float64          `json:"postHitSpeed,omitempty"`
		Location                    *vec3             `json:"location,omitempty"`
		TimeFromCountdownEndSeconds *float64          `json:"timeFromCountdownEndSeconds,omitempty"`
	}{
		Event:                       "_FirstTouch",
		MatchGUID:                   guid,
		Players:                     players,
		PostHitSpeed:                postHitSpeed,
		Location:                    loc,
		TimeFromCountdownEndSeconds: elapsed,
	}
	if b, err := json.Marshal(out); err == nil {
		s.bus.Publish(b)
	}
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

type enrichedCrossbarHit struct {
	Event         string                 `json:"Event"`
	MatchGUID     string                 `json:"matchGuid,omitempty"`
	BallSpeed     *float64               `json:"ballSpeed,omitempty"`
	ImpactForce   *float64               `json:"impactForce,omitempty"`
	BallLocation  *vec3                  `json:"ballLocation,omitempty"`
	BallLastTouch *enrichedBallLastTouch `json:"ballLastTouch,omitempty"`
}

func (s *Synthesizer) onCrossbarHit(raw []byte) {
	inner := unwrapInnerData(raw)
	if inner == "" {
		return
	}
	var d crossbarHitData
	if err := json.Unmarshal([]byte(inner), &d); err != nil {
		return
	}

	guid := pickStr(d.MatchGUID, d.MatchGUIDLow)
	speed := pickFloat(d.BallSpeed, d.BallSpeedLow)
	force := pickFloat(d.ImpactForce, d.ImpactForceLow)
	loc := d.BallLocation
	if loc == nil {
		loc = d.BallLocationLow
	}
	lastTouch := d.BallLastTouch
	if lastTouch == nil {
		lastTouch = d.BallLastTouchLow
	}

	out := enrichedCrossbarHit{
		Event:        "_CrossbarHit",
		MatchGUID:    guid,
		BallSpeed:    speed,
		ImpactForce:  force,
		BallLocation: loc,
	}
	if lastTouch != nil {
		ref := lastTouch.Player
		if ref == nil {
			ref = lastTouch.PlayerLow
		}
		sp := pickFloat(lastTouch.Speed, lastTouch.SpeedLow)
		var enrichedRef *EnrichedPlayer
		if ref != nil {
			enrichedRef = s.roster.ResolveByShortcut(*ref)
		}
		if enrichedRef != nil || sp != nil {
			out.BallLastTouch = &enrichedBallLastTouch{
				Player: enrichedRef,
				Speed:  sp,
			}
		}
	}

	b, err := json.Marshal(out)
	if err != nil {
		return
	}
	s.bus.Publish(b)
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
	s.bus.Publish(b)

	// Begin the _MatchSummary settle window. We cache the current
	// state (winner + final UpdateState snapshot) and start a 2s
	// timer that publishes the summary unless PodiumStart /
	// MatchDestroyed flushes earlier.
	s.beginMatchSummary(guid, winner)
}

// beginMatchSummary captures the final-tick state and starts the
// settle timer. If a summary is already pending (back-to-back
// MatchEnded — shouldn't happen in practice, but defensive), the
// existing one is cancelled and the new one takes over.
func (s *Synthesizer) beginMatchSummary(guid string, winner *int) {
	// Snapshot the latest tick under teamsMu so we don't race with a
	// concurrent UpdateState.
	s.teamsMu.Lock()
	finalSnap := s.lastTick
	s.teamsMu.Unlock()

	s.summaryMu.Lock()
	if s.summaryPending && s.summaryCancel != nil {
		close(s.summaryCancel)
	}
	s.summaryPending = true
	s.summaryGUID = guid
	s.summaryWinner = winner
	s.summaryFinalSnap = finalSnap
	s.summaryMVP = nil
	cancel := make(chan struct{})
	s.summaryCancel = cancel
	s.summaryMu.Unlock()

	go func() {
		select {
		case <-cancel:
			// Flushed early via PodiumStart / MatchDestroyed — that
			// path already published.
		case <-time.After(matchSummarySettleWindow):
			s.flushMatchSummary("settleTimeout")
		}
	}()
}

// flushMatchSummary publishes _MatchSummary using the captured state.
// Idempotent — if no summary is pending, it's a no-op. The trigger
// string lands on the envelope so subscribers can tell which path
// fired the summary.
func (s *Synthesizer) flushMatchSummary(trigger string) {
	s.summaryMu.Lock()
	if !s.summaryPending {
		s.summaryMu.Unlock()
		return
	}
	s.summaryPending = false
	guid := s.summaryGUID
	winner := s.summaryWinner
	finalSnap := s.summaryFinalSnap
	mvp := s.summaryMVP
	cancel := s.summaryCancel
	s.summaryCancel = nil
	s.summaryMu.Unlock()

	if cancel != nil {
		// Wake the goroutine so it doesn't sit on the timer pointlessly.
		// A double-close would panic; cancel is set to nil under the
		// lock above, so subsequent calls can't reach this branch.
		select {
		case <-cancel:
			// already closed (settleTimeout path)
		default:
			close(cancel)
		}
	}

	// Build the summary payload. Winner name + scores from the
	// captured snapshot; players list with full per-player stats so
	// post-game UI can render leaderboards without UpdateState.
	type playerStat struct {
		Player  *EnrichedPlayer `json:"player"`
		Score   int             `json:"score"`
		Goals   int             `json:"goals"`
		Assists int             `json:"assists"`
		Saves   int             `json:"saves"`
		Shots   int             `json:"shots"`
		Demos   int             `json:"demos"`
	}
	var players []playerStat
	scoreBlue, scoreOrange := 0, 0
	winnerName := ""
	if finalSnap != nil {
		scoreBlue = teamScore(finalSnap.teams, 0)
		scoreOrange = teamScore(finalSnap.teams, 1)
		if winner != nil {
			for _, t := range finalSnap.teams {
				if t.TeamNum == *winner {
					winnerName = t.Name
					break
				}
			}
		}
		for _, p := range finalSnap.players {
			players = append(players, playerStat{
				Player: &EnrichedPlayer{
					ID:       p.id,
					Name:     p.name,
					Team:     p.team,
					Platform: platformFromID(p.id),
					IsBot:    isBotId(p.id),
				},
				Score:   p.score,
				Goals:   p.goals,
				Assists: p.assists,
				Saves:   p.saves,
				Shots:   p.shots,
				Demos:   p.demos,
			})
		}
	}

	out := struct {
		Event         string          `json:"Event"`
		MatchGUID     string          `json:"matchGuid,omitempty"`
		WinnerTeamNum *int            `json:"winnerTeamNum,omitempty"`
		WinnerName    string          `json:"winnerName,omitempty"`
		ScoreBlue     int             `json:"scoreBlue"`
		ScoreOrange   int             `json:"scoreOrange"`
		MVP           *EnrichedPlayer `json:"mvp"`
		Players       []playerStat    `json:"players,omitempty"`
		Trigger       string          `json:"trigger"`
	}{
		Event:         "_MatchSummary",
		MatchGUID:     guid,
		WinnerTeamNum: winner,
		WinnerName:    winnerName,
		ScoreBlue:     scoreBlue,
		ScoreOrange:   scoreOrange,
		MVP:           mvp,
		Players:       players,
		Trigger:       trigger,
	}
	if b, err := json.Marshal(out); err == nil {
		s.bus.Publish(b)
	}
}

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
	if scorerRef == nil || (scorerRef.Name == "" && scorerRef.Shortcut == "") {
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

	if assisterRef != nil && (assisterRef.Name != "" || assisterRef.Shortcut != "") {
		out.Assister = s.roster.ResolveByShortcut(*assisterRef)
	}

	if lastTouch != nil {
		ref := lastTouch.Player
		if ref == nil {
			ref = lastTouch.PlayerLow
		}
		sp := pickFloat(lastTouch.Speed, lastTouch.SpeedLow)
		var enrichedRef *EnrichedPlayer
		if ref != nil {
			enrichedRef = s.roster.ResolveByShortcut(*ref)
		}
		if enrichedRef != nil || sp != nil {
			out.BallLastTouch = &enrichedBallLastTouch{
				Player: enrichedRef,
				Speed:  sp,
			}
		}
		// Own-goal heuristic: if the last-touch player is on the
		// conceding team, the goal was deflected/own-goaled. The richer
		// _OwnGoal event ships in Phase 2 with score-delta verification;
		// this flag is the cheap header.
		if enrichedRef != nil && enrichedRef.Team == concedingTeam {
			out.IsOwnGoal = true
		}
	}

	// Modifier flags via the correlation buffer. Statfeeds fire on the
	// same frame as the goal (or one before/after), so a 3-event window
	// is plenty. Match by Shortcut (RL's spectator-name identifier);
	// fall back to Name for safety.
	mods := s.collectGoalModifiers(scorerRef)
	if mods != nil {
		out.Modifiers = mods
	}

	// Record the resolved goal into the correlation buffer so _OwnGoal
	// (Phase 2) can attach it as `correlatedGoal`. The Phase-3 _Assist
	// emitter will also read this entry.
	s.correlation.Record("_GoalScored", &goalRecord{
		Scorer:        scorer,
		ScoringTeam:   scoringTeam,
		ConcedingTeam: concedingTeam,
	})

	b, err := json.Marshal(out)
	if err != nil {
		return
	}
	s.bus.Publish(b)

	// _FirstBlood — first goal of the match.
	s.maybeEmitFirstBlood(guid, scorer, scoringTeam, concedingTeam)
}

func (s *Synthesizer) maybeEmitFirstBlood(guid string, scorer *EnrichedPlayer, scoringTeam, concedingTeam int) {
	s.matchMu.Lock()
	if s.firstBloodFired {
		s.matchMu.Unlock()
		return
	}
	s.firstBloodFired = true
	matchStart := s.matchInitializedAt
	s.matchMu.Unlock()

	var secondsIn *float64
	if !matchStart.IsZero() {
		dur := time.Since(matchStart).Seconds()
		secondsIn = &dur
	}
	out := struct {
		Event             string          `json:"Event"`
		MatchGUID         string          `json:"matchGuid,omitempty"`
		Scorer            *EnrichedPlayer `json:"scorer"`
		ScoringTeam       int             `json:"scoringTeam"`
		ConcedingTeam     int             `json:"concedingTeam"`
		SecondsIntoMatch  *float64        `json:"secondsIntoMatch,omitempty"`
	}{
		Event:            "_FirstBlood",
		MatchGUID:        guid,
		Scorer:           scorer,
		ScoringTeam:      scoringTeam,
		ConcedingTeam:    concedingTeam,
		SecondsIntoMatch: secondsIn,
	}
	if b, err := json.Marshal(out); err == nil {
		s.bus.Publish(b)
	}
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
// Shortcut is the authoritative spectator-name identifier; Name is the
// human-readable form. Either match counts; both empty means no match.
func sameShortcutPlayer(a, b *ShortcutRef) bool {
	if a == nil || b == nil {
		return false
	}
	if a.Shortcut != "" && b.Shortcut != "" && a.Shortcut == b.Shortcut {
		return true
	}
	if a.Name != "" && b.Name != "" && a.Name == b.Name {
		return true
	}
	return false
}
