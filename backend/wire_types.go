package backend

import (
	"rl-toolkit/internal/types"
	"rl-toolkit/internal/wire"
)

// vec3 + wireTeam alias internal/types so future emit subpackages can
// share them. unwrapInnerData / pickStr / pickFloat forward to
// internal/wire helpers.
type vec3 = types.Vec3
type wireTeam = types.WireTeam

var unwrapInnerData = wire.UnwrapInnerData
var pickStr = wire.PickStr
var pickFloat = wire.PickFloat

// Wire-shape types and small helpers shared by multiple emit
// processors. Each type mirrors the JSON RL ships (or the JSON the
// toolkit produces); JSON tags pin the keys.

type teamRef = types.TeamRef

// tickSnapshot is the slice of UpdateState we cache for diff
// emitters. Only fields read by Phase-4 events are kept.
type tickSnapshot struct {
	matchGUID string
	teams     []teamRef
	players   []tickPlayer
	ballTeam  int  // Game.Ball.TeamNum; 255 = untouched
	hasBall   bool // false when Game.Ball was absent
	overtime  bool
	bReplay   bool // Game.bReplay; rising edge marks a goal replay starting
}

// tickPlayer is the per-player slice of UpdateState we cache.
type tickPlayer struct {
	id         string
	name       string
	team       int
	score      int
	goals      int
	assists    int
	saves      int
	shots      int
	touches    int
	carTouches int
	demos      int
	boost      *int // pointer because RL omits in non-spectator mode
	demolished bool
	onGround   bool
	speed      *float64 // pointer: SPECTATOR-only field, omitted otherwise
	supersonic bool
}

// updateStateFull mirrors the wire shape we need for tick diffs.
// Same case-tolerant pattern as the rest of the toolkit: accept
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
	Teams []wireTeam `json:"Teams"`
	Ball  *struct {
		TeamNum int `json:"TeamNum"`
	} `json:"Ball"`
	BOvertime bool `json:"bOvertime"`
	BReplay   bool `json:"bReplay"`
}

type updateStateFullPlayer struct {
	PrimaryID   string   `json:"PrimaryId"`
	Name        string   `json:"Name"`
	TeamNum     int      `json:"TeamNum"`
	Score       int      `json:"Score"`
	Goals       int      `json:"Goals"`
	Assists     int      `json:"Assists"`
	Saves       int      `json:"Saves"`
	Shots       int      `json:"Shots"`
	Touches     int      `json:"Touches"`
	CarTouches  int      `json:"CarTouches"`
	Demos       int      `json:"Demos"`
	Boost       *int     `json:"Boost"`
	Speed       *float64 `json:"Speed"`
	BDemolished bool     `json:"bDemolished"`
	BOnGround   bool     `json:"bOnGround"`
	BSupersonic bool     `json:"bSupersonic"`
}


// ownGoalScoreAfter is part of _OwnGoal's wire shape.
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

// statfeedEnvelope mirrors the StatfeedEvent envelope shape. RL
// ships either PascalCase or all-lowercase keys depending on build.
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

// ballRef is the ball location/speed block found on BallHit and
// CrossbarHit.
type ballRef struct {
	PreHitSpeed  *float64 `json:"PreHitSpeed,omitempty"`
	PostHitSpeed *float64 `json:"PostHitSpeed,omitempty"`
	Location     *vec3    `json:"Location,omitempty"`
}

type ballHitData = types.BallHitData
type ballHitInner = types.BallHitInner

type crossbarHitData = types.CrossbarHitData
type ballLastTouch = types.BallLastTouch
type enrichedBallLastTouch = types.EnrichedBallLastTouch

type matchEndedData = types.MatchEndedData

