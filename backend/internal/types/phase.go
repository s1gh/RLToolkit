package types

// Phase is the canonical gameplay-phase vocabulary used by MatchState
// and the _MatchState synthetic event. Plugins gate their visibility
// on it via the manifest's `whilePhase` array.
type Phase string

const (
	PhaseNone      Phase = "none"
	PhaseLobby     Phase = "lobby"
	PhaseCountdown Phase = "countdown"
	PhaseLive      Phase = "live"
	PhasePaused    Phase = "paused"
	PhaseReplay    Phase = "replay"
	PhaseEnded     Phase = "ended"
	PhasePodium    Phase = "podium"
)
