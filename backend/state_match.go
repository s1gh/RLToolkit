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

	bus Broadcaster // optional; if set, the watchdog publishes directly
}

func (m *MatchState) AttachBroadcaster(b Broadcaster) { m.bus = b }

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
	// Hot path: UpdateState. Track last-tick time, do replay-edge
	// detection, opportunistically capture matchGuid on first tick of
	// a fresh connection.
	if evt.Name == "UpdateState" {
		m.lastTickMu.Lock()
		m.lastTick = time.Now()
		m.lastTickMu.Unlock()

		wasReplay := m.inReplay.Load()
		nowReplay := scanBReplay(evt.Raw)
		if nowReplay != wasReplay {
			m.inReplay.Store(nowReplay)
			if nowReplay {
				m.transitionIf(PhasePhaseReplay, "bReplayEdge", "", func(cur Phase) bool {
					return cur == PhasePhaseLive || cur == PhasePhaseCountdown || cur == PhasePhasePaused
				})
			} else {
				m.transitionIf(PhasePhaseLive, "bReplayEdge", "", func(cur Phase) bool {
					return cur == PhasePhaseReplay
				})
			}
		}

		if !m.matchActive.Load() {
			guid := extractMatchGUID(evt.Raw)
			m.transitionTo(PhasePhaseLive, "UpdateState", guid, true)
		}
		return
	}

	switch evt.Name {
	case "MatchCreated":
		m.transitionTo(PhasePhaseLobby, "MatchCreated", guidFromData(evt.Data), true)
	case "CountdownBegin":
		m.transitionTo(PhasePhaseCountdown, "CountdownBegin", "", true)
	case "RoundStarted":
		m.transitionTo(PhasePhaseLive, "RoundStarted", "", true)
	case "MatchPaused":
		m.transitionTo(PhasePhasePaused, "MatchPaused", "", true)
	case "MatchUnpaused":
		m.transitionTo(PhasePhaseLive, "MatchUnpaused", "", true)
	case "MatchEnded":
		m.inReplay.Store(false)
		m.transitionTo(PhasePhaseEnded, "MatchEnded", "", true)
	case "PodiumStart":
		m.inReplay.Store(false)
		m.transitionTo(PhasePhasePodium, "PodiumStart", "", true)
	case "MatchDestroyed":
		m.inReplay.Store(false)
		m.transitionTo(PhasePhaseNone, "MatchDestroyed", "clear", false)
	case "_ConnectionStatus":
		var env struct {
			Status string `json:"Status"`
		}
		if err := json.Unmarshal(evt.Raw, &env); err == nil && env.Status != "" && env.Status != string(StatusConnected) {
			m.inReplay.Store(false)
			m.transitionTo(PhasePhaseNone, "connectionLost", "clear", false)
		}
	}
}

// transitionTo unconditionally moves to the given phase. If
// keepActive is true, sets matchActive=true; if false, sets
// matchActive=false and (when guid=="clear") clears the matchGuid.
// Stages a pending _MatchState snapshot whenever the snapshot
// actually changes.
func (m *MatchState) transitionTo(phase Phase, trigger, guid string, keepActive bool) {
	m.mu.Lock()
	prev := m.cur
	next := prev
	next.Phase = phase
	next.MatchActive = keepActive
	if !keepActive && guid == "clear" {
		next.MatchGuid = ""
	} else if guid != "" {
		next.MatchGuid = guid
	}
	if next == prev {
		m.mu.Unlock()
		return
	}
	now := time.Now()
	dur := now.Sub(prev.Since).Seconds()
	next.PreviousPhase = prev.Phase
	next.PhaseDurationSeconds = dur
	next.Since = now
	next.Trigger = trigger
	m.cur = next
	m.pending = next
	m.emit = true
	m.matchActive.Store(next.MatchActive)
	m.mu.Unlock()
}

// transitionIf only transitions when the predicate accepts the current
// phase. Used for replay-edge transitions that must not clobber a
// same-tick CountdownBegin or override post-match phases.
func (m *MatchState) transitionIf(phase Phase, trigger, guid string, allow func(cur Phase) bool) {
	m.mu.Lock()
	if !allow(m.cur.Phase) {
		m.mu.Unlock()
		return
	}
	prev := m.cur
	next := prev
	next.Phase = phase
	if guid != "" {
		next.MatchGuid = guid
	}
	if next == prev {
		m.mu.Unlock()
		return
	}
	now := time.Now()
	next.PreviousPhase = prev.Phase
	next.PhaseDurationSeconds = now.Sub(prev.Since).Seconds()
	next.Since = now
	next.Trigger = trigger
	m.cur = next
	m.pending = next
	m.emit = true
	m.mu.Unlock()
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
	if !m.matchActive.Load() {
		return
	}
	m.lastTickMu.Lock()
	last := m.lastTick
	m.lastTickMu.Unlock()
	if last.IsZero() || time.Since(last) < m.timeout {
		return
	}
	m.transitionTo(PhasePhaseNone, "watchdogTimeout", "clear", false)

	// Publish directly so subscribers learn about the timeout even
	// when no events are flowing through the dispatcher.
	if m.bus != nil {
		evts := m.Process(Event{})
		for _, evt := range evts {
			body := evt.Data
			envelope, err := json.Marshal(struct {
				Event string          `json:"Event"`
				Data  json.RawMessage `json:"Data"`
			}{Event: evt.Name, Data: body})
			if err == nil {
				m.bus.Broadcast(Event{Name: evt.Name, Raw: envelope})
			}
		}
	}
}
