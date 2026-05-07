package backend

import "rl-toolkit/backend/internal/correlation"

// CorrelationBuffer is a thin alias over the extracted ring buffer in
// internal/correlation. Kept under the original name so downstream
// emit/state/server callers don't have to change. The typed records
// (ballHitRecord, goalRecord, …) live in correlation_types.go because
// they reference EnrichedPlayer, which is still defined in this
// package.
type CorrelationBuffer = correlation.Buffer

// NewCorrelationBuffer is the original constructor surface. The
// underlying call now goes through the internal package.
func NewCorrelationBuffer(capacity int) *CorrelationBuffer {
	return correlation.New(capacity)
}
