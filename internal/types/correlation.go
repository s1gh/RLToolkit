package types

// Correlation-buffer record types. Multiple emitters share these:
// BallHit writes BallHitRecord; OwnGoal/Statfeed/Goal read it.
// Lives here (rather than in internal/correlation) because the
// records reference EnrichedPlayer, which already lives in this
// package — internal/correlation is the buffer machinery, not the
// payload schema.

// BallHitRecord is the correlation-buffer entry for a BallHit.
// Carries the resolved primary toucher plus the scalar speed fields
// from the envelope so downstream emitters (_Shot / _Center / _Clear /
// _BicycleHit / _BallPossessionChanged) can attach them without
// re-parsing.
type BallHitRecord struct {
	Player       *EnrichedPlayer
	PreHitSpeed  *float64
	PostHitSpeed *float64
}

// GoalRecord is the slim correlation-buffer entry for _GoalScored —
// just the bits OwnGoal / Assist need to relate themselves to a goal.
type GoalRecord struct {
	Scorer        *EnrichedPlayer
	ScoringTeam   int
	ConcedingTeam int
}

// StatfeedRecord is what the correlation buffer holds for each
// StatfeedEvent. Only the fields _GoalScored / Phase-3 emitters look
// at are kept — small footprint per entry.
type StatfeedRecord struct {
	EventName string
	MainRef   *ShortcutRef
	Resolved  *EnrichedPlayer
}

// EnrichedCorrelatedTouch is the wire shape for `correlatedTouch` on
// touch-variant events (_Shot / _Center / _Clear / _BicycleHit) and
// on _BallPossessionChanged.triggeredBy. Speeds are nullable since RL
// omits them on some BallHit envelopes.
type EnrichedCorrelatedTouch struct {
	Player       *EnrichedPlayer `json:"player,omitempty"`
	PreHitSpeed  *float64        `json:"preHitSpeed,omitempty"`
	PostHitSpeed *float64        `json:"postHitSpeed,omitempty"`
}
