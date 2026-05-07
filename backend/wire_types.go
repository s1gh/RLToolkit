package backend

import (
	"rl-toolkit/internal/types"
	"rl-toolkit/internal/wire"
)

// Aliases over internal/types for the wire shapes shared with the
// emit subpackage; small forwarders to internal/wire for the
// case-tolerant decode helpers. tickSnapshot/tickPlayer/TickStore now
// live in tick_alias.go.
type vec3 = types.Vec3
type wireTeam = types.WireTeam
type teamRef = types.TeamRef
type ballHitData = types.BallHitData
type ballHitInner = types.BallHitInner
type crossbarHitData = types.CrossbarHitData
type ballLastTouch = types.BallLastTouch
type enrichedBallLastTouch = types.EnrichedBallLastTouch
type matchEndedData = types.MatchEndedData
type ownGoalScoreAfter = types.OwnGoalScoreAfter

var unwrapInnerData = wire.UnwrapInnerData
var pickStr = wire.PickStr
var pickFloat = wire.PickFloat
var teamScore = types.TeamScore

// statfeedEnvelope mirrors the StatfeedEvent envelope shape. RL
// ships either PascalCase or all-lowercase keys depending on build.
// Stays in backend until StatfeedEmitter migrates.
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
// CrossbarHit. Kept here only for any in-backend code referencing it
// (currently none — candidates for cleanup once goal/own_goal migrate).
type ballRef struct {
	PreHitSpeed  *float64 `json:"PreHitSpeed,omitempty"`
	PostHitSpeed *float64 `json:"PostHitSpeed,omitempty"`
	Location     *vec3    `json:"Location,omitempty"`
}
