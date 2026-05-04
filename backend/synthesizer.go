package main

import (
	"encoding/json"
)

// Synthesizer turns raw RL events into _-prefixed synthetic events with
// pre-resolved player references and other server-side enrichment. One
// instance is attached to the RL client alongside the trackers; Feed is
// called from the dispatcher after the roster tracker has digested the
// envelope, so player resolution sees the up-to-date roster.
type Synthesizer struct {
	bus    *EventBus
	roster *RosterTracker
}

func NewSynthesizer(bus *EventBus, roster *RosterTracker) *Synthesizer {
	return &Synthesizer{bus: bus, roster: roster}
}

// Feed inspects each envelope and dispatches to the per-event synthesizer.
// Cheap when the event isn't one we synthesize (single name compare).
func (s *Synthesizer) Feed(raw []byte) {
	name := extractEventName(raw)
	switch name {
	case "StatfeedEvent":
		s.onStatfeedEvent(raw)
	case "BallHit":
		s.onBallHit(raw)
	case "CrossbarHit":
		s.onCrossbarHit(raw)
	}
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
