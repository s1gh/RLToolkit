package types

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
