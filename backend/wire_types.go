package backend

import "encoding/json"

// Wire-shape types and small helpers shared by multiple emit
// processors. Each type mirrors the JSON RL ships (or the JSON the
// toolkit produces); JSON tags pin the keys.

// teamRef is the slice of UpdateState.Game.Teams[] cached for
// downstream enrichment. Score is here for diff events; only Name
// and TeamNum are read by _MatchEnded.
type teamRef struct {
	TeamNum        int
	Name           string
	Score          int
	ColorPrimary   string
	ColorSecondary string
}

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
	boost      *int     // pointer because RL omits in non-spectator mode
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

type wireTeam struct {
	TeamNum        int    `json:"TeamNum"`
	Name           string `json:"Name"`
	Score          int    `json:"Score"`
	ColorPrimary   string `json:"ColorPrimary"`
	ColorSecondary string `json:"ColorSecondary"`
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

// vec3 is the 3D location/vector shape RL ships on BallHit /
// CrossbarHit / GoalScored.
type vec3 struct {
	X float64 `json:"X"`
	Y float64 `json:"Y"`
	Z float64 `json:"Z"`
}

// ballRef is the ball location/speed block found on BallHit and
// CrossbarHit.
type ballRef struct {
	PreHitSpeed  *float64 `json:"PreHitSpeed,omitempty"`
	PostHitSpeed *float64 `json:"PostHitSpeed,omitempty"`
	Location     *vec3    `json:"Location,omitempty"`
}

type ballHitData struct {
	MatchGUID    string        `json:"MatchGuid"`
	MatchGUIDLow string        `json:"matchguid"`
	Players      []ShortcutRef `json:"Players"`
	PlayersLow   []ShortcutRef `json:"players"`
	Ball         *ballHitInner `json:"Ball"`
	BallLow      *ballHitInner `json:"ball"`
}

type ballHitInner struct {
	PreHitSpeed  *float64 `json:"PreHitSpeed"`
	PostHitSpeed *float64 `json:"PostHitSpeed"`
	Location     *vec3    `json:"Location"`
}

// crossbarHitData mirrors the wire shape of a CrossbarHit envelope.
// BallLastTouch is the player who last touched the ball before it
// hit the crossbar — typically the shooter.
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

// matchEndedData mirrors the wire shape of MatchEnded.
type matchEndedData struct {
	MatchGUID     string `json:"MatchGuid"`
	MatchGUIDLow  string `json:"matchguid"`
	WinnerTeamNum *int   `json:"WinnerTeamNum"`
	WinnerTeamLow *int   `json:"winnerteamnum"`
}

// unwrapInnerData pulls the inner Data string out of an envelope,
// accepting both PascalCase and lowercase top-level keys. Returns ""
// on missing or malformed envelope.
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

// pickStr returns a if non-empty, otherwise b. Used to pick between
// PascalCase and lowercase JSON values.
func pickStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// pickFloat returns a if non-nil, otherwise b.
func pickFloat(a, b *float64) *float64 {
	if a != nil {
		return a
	}
	return b
}
