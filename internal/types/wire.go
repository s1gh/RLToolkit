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
