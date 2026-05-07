package backend

import "rl-toolkit/backend/internal/types"

// Correlation-buffer record types now live in internal/types so emit
// subpackages can read/write them without depending on backend.
type ballHitRecord = types.BallHitRecord
type goalRecord = types.GoalRecord
type statfeedRecord = types.StatfeedRecord
type enrichedCorrelatedTouch = types.EnrichedCorrelatedTouch
