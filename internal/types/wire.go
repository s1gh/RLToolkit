package types

// TeamRef is the slim per-team record cached by TickStore for
// downstream enrichment. Mirrors the fields that come off
// UpdateState.Game.Teams[]; TickStore decodes once so consumers don't
// each re-parse on every tick. No JSON tags on purpose — this never
// leaves the toolkit on the wire.
type TeamRef struct {
	TeamNum        int
	Name           string
	Score          int
	ColorPrimary   string
	ColorSecondary string
}

// TickSnapshot is the slim per-tick projection of UpdateState that
// TickStore caches once per envelope. Diff emitters compare
// (Previous, Latest) snapshots; gate emitters read individual flags.
type TickSnapshot struct {
	MatchGUID string
	Teams     []TeamRef
	Players   []TickPlayer
	BallTeam  int  // Game.Ball.TeamNum; 255 = untouched
	HasBall   bool // false when Game.Ball was absent
	Overtime  bool
	BReplay   bool // Game.bReplay; rising edge marks a goal replay starting
}

// TickPlayer is the per-player slice of UpdateState we cache.
type TickPlayer struct {
	ID         string
	Name       string
	Team       int
	Score      int
	Goals      int
	Assists    int
	Saves      int
	Shots      int
	Touches    int
	CarTouches int
	Demos      int
	Boost      *int // pointer because RL omits in non-spectator mode
	Demolished bool
	OnGround   bool
	Speed      *float64 // pointer: SPECTATOR-only field, omitted otherwise
	Supersonic bool
}

// TeamScore reads the Score for a TeamNum out of a Teams[] snapshot,
// returning 0 if the team isn't present. Used by emit/state code that
// builds blue/orange tallies for derived events.
func TeamScore(teams []TeamRef, num int) int {
	for _, t := range teams {
		if t.TeamNum == num {
			return t.Score
		}
	}
	return 0
}

// Vec3 is the 3D location/vector shape RL ships on BallHit /
// CrossbarHit / GoalScored.
type Vec3 struct {
	X float64 `json:"X"`
	Y float64 `json:"Y"`
	Z float64 `json:"Z"`
}

// WireTeam mirrors the Teams[] block on UpdateState and on a few of
// the typed events. Score is here for diff/ot detection; Name and
// TeamNum are read by _MatchEnded.
type WireTeam struct {
	TeamNum        int    `json:"TeamNum"`
	Name           string `json:"Name"`
	Score          int    `json:"Score"`
	ColorPrimary   string `json:"ColorPrimary"`
	ColorSecondary string `json:"ColorSecondary"`
}

// CrossbarHitData mirrors the wire shape of a CrossbarHit envelope.
// BallLastTouch is the player who last touched the ball before it
// hit the crossbar — typically the shooter. RL ships either
// PascalCase or all-lowercase keys depending on build, hence each
// field appearing twice.
type CrossbarHitData struct {
	MatchGUID        string         `json:"MatchGuid"`
	MatchGUIDLow     string         `json:"matchguid"`
	BallSpeed        *float64       `json:"BallSpeed"`
	BallSpeedLow     *float64       `json:"ballspeed"`
	ImpactForce      *float64       `json:"ImpactForce"`
	ImpactForceLow   *float64       `json:"impactforce"`
	BallLocation     *Vec3          `json:"BallLocation"`
	BallLocationLow  *Vec3          `json:"balllocation"`
	BallLastTouch    *BallLastTouch `json:"BallLastTouch"`
	BallLastTouchLow *BallLastTouch `json:"balllasttouch"`
}

// BallLastTouch is the wire-shape "who last touched the ball" stub
// found inside CrossbarHit envelopes.
type BallLastTouch struct {
	Player    *ShortcutRef `json:"Player"`
	PlayerLow *ShortcutRef `json:"player"`
	Speed     *float64     `json:"Speed"`
	SpeedLow  *float64     `json:"speed"`
}

// EnrichedBallLastTouch is the toolkit-side projection of
// BallLastTouch with the player resolved against the live roster.
// Shipped on _CrossbarHit.
type EnrichedBallLastTouch struct {
	Player *EnrichedPlayer `json:"player,omitempty"`
	Speed  *float64        `json:"speed,omitempty"`
}

// MatchEndedData mirrors the wire shape of MatchEnded.
type MatchEndedData struct {
	MatchGUID     string `json:"MatchGuid"`
	MatchGUIDLow  string `json:"matchguid"`
	WinnerTeamNum *int   `json:"WinnerTeamNum"`
	WinnerTeamLow *int   `json:"winnerteamnum"`
}

// BallHitData mirrors the wire shape of a BallHit envelope. RL ships
// either PascalCase or all-lowercase keys depending on build, hence
// each field appearing twice.
type BallHitData struct {
	MatchGUID    string        `json:"MatchGuid"`
	MatchGUIDLow string        `json:"matchguid"`
	Players      []ShortcutRef `json:"Players"`
	PlayersLow   []ShortcutRef `json:"players"`
	Ball         *BallHitInner `json:"Ball"`
	BallLow      *BallHitInner `json:"ball"`
}

type BallHitInner struct {
	PreHitSpeed  *float64 `json:"PreHitSpeed"`
	PostHitSpeed *float64 `json:"PostHitSpeed"`
	Location     *Vec3    `json:"Location"`
}

// OwnGoalScoreAfter is part of _OwnGoal's wire shape — the per-team
// score state immediately after the deflection.
type OwnGoalScoreAfter struct {
	Blue   int `json:"blue"`
	Orange int `json:"orange"`
}

// StatfeedEnvelope mirrors the StatfeedEvent envelope shape. RL ships
// either PascalCase or all-lowercase keys depending on build, hence
// each field appearing twice.
type StatfeedEnvelope struct {
	Data    string `json:"Data"`
	DataLow string `json:"data"`
}

// StatfeedData is the inner payload shape of StatfeedEvent.
type StatfeedData struct {
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
