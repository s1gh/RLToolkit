package main

// Correlation-buffer record types and shared wire fragments. Lives in
// its own file because multiple emit processors (BallHit writes,
// OwnGoal/Statfeed/Goal read) plus the legacy Synthesizer all touch
// these.

// ballHitRecord is the correlation-buffer entry for a BallHit. Carries
// the resolved primary toucher plus the scalar speed fields from the
// envelope so downstream emitters (_Shot / _Center / _Clear /
// _BicycleHit / _BallPossessionChanged) can attach them without
// re-parsing.
type ballHitRecord struct {
	Player       *EnrichedPlayer
	PreHitSpeed  *float64
	PostHitSpeed *float64
}

// statfeedRecord is what the correlation buffer holds for each
// StatfeedEvent. Only the fields _GoalScored / Phase-3 emitters look
// at are kept — small footprint per entry.
type statfeedRecord struct {
	EventName string
	MainRef   *ShortcutRef
	Resolved  *EnrichedPlayer
}

// enrichedCorrelatedTouch is the wire shape for `correlatedTouch` on
// touch-variant events (_Shot / _Center / _Clear / _BicycleHit) and
// on _BallPossessionChanged.triggeredBy. Speeds are nullable since RL
// omits them on some BallHit envelopes.
type enrichedCorrelatedTouch struct {
	Player       *EnrichedPlayer `json:"player,omitempty"`
	PreHitSpeed  *float64        `json:"preHitSpeed,omitempty"`
	PostHitSpeed *float64        `json:"postHitSpeed,omitempty"`
}
