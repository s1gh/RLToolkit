package main

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"time"
)

// Phase is the canonical gameplay-phase vocabulary used by MatchState
// and the _MatchState synthetic event. Distinct from the (now-legacy)
// LifecyclePhase so the two systems can coexist during the migration.
type Phase string

const (
	PhasePhaseNone      Phase = "none"
	PhasePhaseLobby     Phase = "lobby"
	PhasePhaseCountdown Phase = "countdown"
	PhasePhaseLive      Phase = "live"
	PhasePhasePaused    Phase = "paused"
	PhasePhaseReplay    Phase = "replay"
	PhasePhaseEnded     Phase = "ended"
	PhasePhasePodium    Phase = "podium"
)

// MatchStateSnapshot is the wire-shaped payload published as
// _MatchState (and exposed by /api/match-state).
type MatchStateSnapshot struct {
	MatchActive          bool      `json:"matchActive"`
	Phase                Phase     `json:"phase"`
	PreviousPhase        Phase     `json:"previousPhase"`
	MatchGuid            string    `json:"matchGuid,omitempty"`
	Since                time.Time `json:"since"`
	PhaseDurationSeconds float64   `json:"phaseDurationSeconds"`
	Trigger              string    `json:"trigger"`
}

// matchActiveTimeoutNew is how long we tolerate silence on the
// UpdateState stream before declaring matchActive=false. Same value as
// the legacy tracker; renamed to avoid a collision during the
// parallel-running stage.
const matchActiveTimeoutNew = 5 * time.Second

// MatchState is the unified gameplay-state machine. Replaces
// LifecycleTracker and PhaseMachine. Implements both StateProcessor
// (Observe) and EmitProcessor (Process) — the same code knows what
// changed and emits _MatchState only on real transitions.
type MatchState struct {
	mu      sync.RWMutex
	cur     MatchStateSnapshot
	emit    bool               // set by Observe when state changed; consumed by Process
	pending MatchStateSnapshot // the snapshot to emit on the next Process call

	matchActive atomic.Bool
	inReplay    atomic.Bool

	lastTickMu sync.Mutex
	lastTick   time.Time

	timeout time.Duration
}

func NewMatchState() *MatchState {
	now := time.Now()
	return &MatchState{
		cur: MatchStateSnapshot{
			MatchActive:   false,
			Phase:         PhasePhaseNone,
			PreviousPhase: PhasePhaseNone,
			Since:         now,
			Trigger:       "initial",
		},
		timeout: matchActiveTimeoutNew,
	}
}

func (m *MatchState) Snapshot() MatchStateSnapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cur
}

// Observe is the StateProcessor entry point. It updates internal state
// based on the event. If the state actually changed, it stages a
// pending snapshot that the next Process call will return.
func (m *MatchState) Observe(evt Event) {
	// Implementation in Task 2.2.
}

// Process is the EmitProcessor entry point. Returns the staged
// _MatchState event if Observe set one; nil otherwise.
func (m *MatchState) Process(evt Event) []Event {
	m.mu.Lock()
	if !m.emit {
		m.mu.Unlock()
		return nil
	}
	pending := m.pending
	m.emit = false
	m.mu.Unlock()
	body, err := json.Marshal(pending)
	if err != nil {
		return nil
	}
	return []Event{{
		Name: "_MatchState",
		Data: body,
	}}
}

// Run starts the watchdog goroutine that flips matchActive=false when
// no UpdateState has arrived within the timeout. Blocks until ctx is
// cancelled.
func (m *MatchState) Run(ctx context.Context) {
	tick := time.NewTicker(m.timeout / 2)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			m.checkTimeout()
		}
	}
}

func (m *MatchState) checkTimeout() {
	// Implementation in Task 2.3.
}
