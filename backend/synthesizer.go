package main

import (
	"encoding/json"
	"sync"
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

	// teamsMu guards lastTeams. Cached from the most recent UpdateState
	// so _MatchEnded can resolve WinnerTeamNum to a team name without a
	// second round-trip. Grows in Phase 4 (diff events).
	teamsMu   sync.Mutex
	lastTeams []teamRef

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
}

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
	}
}

// onUpdateState caches the latest Teams[] for downstream enrichment and
// runs score-delta detection for _OwnGoal. Decodes only Game.Teams; the
// full UpdateState is decoded elsewhere when needed. This must stay
// cheap — runs at PacketSendRate.
func (s *Synthesizer) onUpdateState(raw []byte) {
	inner := unwrapInnerData(raw)
	if inner == "" {
		return
	}
	var d struct {
		MatchGUID    string     `json:"MatchGuid"`
		MatchGUIDLow string     `json:"matchguid"`
		Game         *gameTeams `json:"Game"`
		GameLow      *gameTeams `json:"game"`
	}
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

	// Compute per-team score deltas against the prior cache before we
	// overwrite. A delta of +1 with a recent ball touch by the opposing
	// team is the own-goal signature. A team going +1 with no recent
	// ball touch (forfeit, mercy rule) does not trigger.
	s.teamsMu.Lock()
	prev := s.lastTeams
	s.lastTeams = teams
	s.teamsMu.Unlock()

	if len(prev) > 0 {
		guid := pickStr(d.MatchGUID, d.MatchGUIDLow)
		s.detectOwnGoal(prev, teams, guid)
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
