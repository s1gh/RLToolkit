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

	// teamsMu guards lastTeams. Cached from the most recent UpdateState
	// so _MatchEnded can resolve WinnerTeamNum to a team name without a
	// second round-trip. Grows in Phase 4 (diff events).
	teamsMu   sync.Mutex
	lastTeams []teamRef
}

func NewSynthesizer(bus *EventBus, roster *RosterTracker) *Synthesizer {
	return &Synthesizer{bus: bus, roster: roster}
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

// onUpdateState caches the latest Teams[] for downstream enrichment.
// We only decode Game.Teams here; the full UpdateState is decoded
// elsewhere when needed. This must stay cheap — runs at PacketSendRate.
func (s *Synthesizer) onUpdateState(raw []byte) {
	inner := unwrapInnerData(raw)
	if inner == "" {
		return
	}
	var d struct {
		Game    *gameTeams `json:"Game"`
		GameLow *gameTeams `json:"game"`
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
	s.teamsMu.Lock()
	s.lastTeams = teams
	s.teamsMu.Unlock()
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

	b, err := json.Marshal(out)
	if err != nil {
		return
	}
	s.bus.Publish(b)
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

	b, err := json.Marshal(out)
	if err != nil {
		return
	}
	s.bus.Publish(b)
}
