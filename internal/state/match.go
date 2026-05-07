// Package state owns MatchState — the unified gameplay-phase
// machine. Implements both StateProcessor (Observe) and EmitProcessor
// (Process) for the pipeline; the watchdog goroutine (Run) flips
// matchActive=false on UpdateState silence.
package state

import (
	"context"
	"encoding/json"
	"rl-toolkit/internal/bus"
	"rl-toolkit/internal/types"
	"rl-toolkit/internal/wire"
	"sync"
	"sync/atomic"
	"time"
)

// Snapshot is the wire-shaped payload published as _MatchState (and
// exposed by /api/match-state).
type Snapshot struct {
	MatchActive          bool        `json:"matchActive"`
	Phase                types.Phase `json:"phase"`
	PreviousPhase        types.Phase `json:"previousPhase"`
	MatchGuid            string      `json:"matchGuid,omitempty"`
	Since                time.Time   `json:"since"`
	PhaseDurationSeconds float64     `json:"phaseDurationSeconds"`
	Trigger              string      `json:"trigger"`
}

// matchActiveTimeout is how long we tolerate silence on the
// UpdateState stream before declaring matchActive=false.
const matchActiveTimeout = 5 * time.Second

// connectedStatus is the literal wire string that _ConnectionStatus
// carries when the RL TCP socket is healthy. Anything else (including
// the explicit empty value) means "lost connection" and clears
// matchActive — same effect the source's setStatus produces.
const connectedStatus = "connected"

// Broadcaster is the slim interface the watchdog uses when it has to
// publish a _MatchState frame outside the normal dispatcher flow.
type Broadcaster interface {
	Broadcast(evt bus.Event)
}

// MatchState is the unified gameplay-state machine. The same code
// observes events, decides whether anything changed, and emits a
// _MatchState frame on real transitions only.
type MatchState struct {
	mu      sync.RWMutex
	cur     Snapshot
	emit    bool     // set by Observe when state changed; consumed by Process
	pending Snapshot // the snapshot to emit on the next Process call

	matchActive atomic.Bool
	inReplay    atomic.Bool

	lastTickMu sync.Mutex
	lastTick   time.Time

	timeout time.Duration

	bus Broadcaster // optional; if set, the watchdog publishes directly
}

// AttachBroadcaster wires a publisher into the watchdog so timeout
// frames reach subscribers even when no events are flowing through
// the dispatcher.
func (m *MatchState) AttachBroadcaster(b Broadcaster) { m.bus = b }

func New() *MatchState {
	now := time.Now()
	return &MatchState{
		cur: Snapshot{
			MatchActive:   false,
			Phase:         types.PhaseNone,
			PreviousPhase: types.PhaseNone,
			Since:         now,
			Trigger:       "initial",
		},
		timeout: matchActiveTimeout,
	}
}

func (m *MatchState) Snapshot() Snapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cur
}

// Observe is the StateProcessor entry point. It updates internal state
// based on the event. If the state actually changed, it stages a
// pending snapshot that the next Process call will return.
func (m *MatchState) Observe(evt bus.Event) {
	// Hot path: UpdateState. Track last-tick time, do replay-edge
	// detection, opportunistically capture matchGuid on first tick of
	// a fresh connection.
	if evt.Name == "UpdateState" {
		m.lastTickMu.Lock()
		m.lastTick = time.Now()
		m.lastTickMu.Unlock()

		wasReplay := m.inReplay.Load()
		nowReplay := wire.ScanBReplay(evt.Raw)
		if nowReplay != wasReplay {
			m.inReplay.Store(nowReplay)
			if nowReplay {
				m.transitionIf(types.PhaseReplay, "bReplayEdge", "", func(cur types.Phase) bool {
					return cur == types.PhaseLive || cur == types.PhaseCountdown || cur == types.PhasePaused
				})
			} else {
				m.transitionIf(types.PhaseLive, "bReplayEdge", "", func(cur types.Phase) bool {
					return cur == types.PhaseReplay
				})
			}
		}

		if !m.matchActive.Load() {
			guid := wire.ExtractMatchGUID(evt.Raw)
			m.transitionTo(types.PhaseLive, "UpdateState", guid, true)
		}
		return
	}

	switch evt.Name {
	case "MatchCreated":
		m.transitionTo(types.PhaseLobby, "MatchCreated", wire.GUIDFromData(evt.Data), true)
	case "CountdownBegin":
		m.transitionTo(types.PhaseCountdown, "CountdownBegin", "", true)
	case "RoundStarted":
		m.transitionTo(types.PhaseLive, "RoundStarted", "", true)
	case "MatchPaused":
		m.transitionTo(types.PhasePaused, "MatchPaused", "", true)
	case "MatchUnpaused":
		m.transitionTo(types.PhaseLive, "MatchUnpaused", "", true)
	case "MatchEnded":
		m.inReplay.Store(false)
		m.transitionTo(types.PhaseEnded, "MatchEnded", "", true)
	case "PodiumStart":
		m.inReplay.Store(false)
		m.transitionTo(types.PhasePodium, "PodiumStart", "", true)
	case "MatchDestroyed":
		m.inReplay.Store(false)
		m.transitionTo(types.PhaseNone, "MatchDestroyed", "clear", false)
	case "_ConnectionStatus":
		var env struct {
			Status string `json:"Status"`
		}
		if err := json.Unmarshal(evt.Raw, &env); err == nil && env.Status != "" && env.Status != connectedStatus {
			m.inReplay.Store(false)
			m.transitionTo(types.PhaseNone, "connectionLost", "clear", false)
		}
	}
}

// transitionTo unconditionally moves to the given phase. If
// keepActive is true, sets matchActive=true; if false, sets
// matchActive=false and (when guid=="clear") clears the matchGuid.
// Stages a pending _MatchState snapshot whenever the snapshot
// actually changes.
func (m *MatchState) transitionTo(phase types.Phase, trigger, guid string, keepActive bool) {
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
func (m *MatchState) transitionIf(phase types.Phase, trigger, guid string, allow func(cur types.Phase) bool) {
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
func (m *MatchState) Process(evt bus.Event) []bus.Event {
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
	return []bus.Event{{
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
	m.transitionTo(types.PhaseNone, "watchdogTimeout", "clear", false)

	// Publish directly so subscribers learn about the timeout even
	// when no events are flowing through the dispatcher.
	if m.bus != nil {
		evts := m.Process(bus.Event{})
		for _, evt := range evts {
			body := evt.Data
			envelope, err := json.Marshal(struct {
				Event string          `json:"Event"`
				Data  json.RawMessage `json:"Data"`
			}{Event: evt.Name, Data: body})
			if err == nil {
				m.bus.Broadcast(bus.Event{Name: evt.Name, Raw: envelope})
			}
		}
	}
}
