package backend

import "rl-toolkit/internal/state"

// MatchState + MatchStateSnapshot alias internal/state so emit, server,
// and main keep working unchanged. NewMatchState preserves the original
// constructor name. The backend's existing Broadcaster alias (over
// internal/pipeline.Broadcaster) is structurally compatible with
// state.Broadcaster, so AttachBroadcaster continues to accept it.
type MatchState = state.MatchState
type MatchStateSnapshot = state.Snapshot

func NewMatchState() *MatchState {
	return state.New()
}
