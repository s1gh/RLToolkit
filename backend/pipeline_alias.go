package backend

import "rl-toolkit/backend/internal/pipeline"

// Pipeline is an alias over the extracted pipeline orchestrator. The
// processor interfaces are satisfied structurally — no need to alias
// them since callers (RLSource, FixtureSource, the emit processors)
// implement the methods directly. Broadcaster is aliased because
// MatchState.AttachBroadcaster takes it as an explicit parameter type.
//
// NewPipeline is preserved as a thin shim so main.go's wiring stays
// unchanged.
type Pipeline = pipeline.Pipeline
type Broadcaster = pipeline.Broadcaster

func NewPipeline() *Pipeline {
	return pipeline.New()
}
