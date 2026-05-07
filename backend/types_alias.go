package backend

import "rl-toolkit/backend/internal/types"

// Phase + the eight phase constants alias internal/types so emit,
// catalog, server, and main keep their existing PhaseLive/PhaseLobby
// spellings without an additional import.
type Phase = types.Phase

const (
	PhaseNone      = types.PhaseNone
	PhaseLobby     = types.PhaseLobby
	PhaseCountdown = types.PhaseCountdown
	PhaseLive      = types.PhaseLive
	PhasePaused    = types.PhasePaused
	PhaseReplay    = types.PhaseReplay
	PhaseEnded     = types.PhaseEnded
	PhasePodium    = types.PhasePodium
)
