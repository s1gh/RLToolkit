// Package state owns MatchState — the unified gameplay-phase
// machine. Implements both StateProcessor (Observe) and EmitProcessor
// (Process) for the pipeline; the watchdog goroutine (Run) flips
// matchActive=false on UpdateState silence.
package state

import (
	"context"
	"encoding/json"
	"rl-toolkit/backend/internal/bus"
	"rl-toolkit/backend/internal/types"
	"rl-toolkit/backend/internal/wire"
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

	// lastStartedGuid is the matchGuid the most recent _MatchStarted
	// envelope reported for. Used to fire _MatchStarted exactly once
	// per new match — the first time a non-empty guid lands a player
	// into Countdown or Live. Cleared on MatchDestroyed so a reconnect
	// or replay of the same guid still emits.
	lastStartedGuid string
	// lastEndedGuid is the matchGuid the most recent wire MatchEnded
	// reported for. Used by the _MatchAbandoned gate so a clean
	// match-end followed by MatchDestroyed is recognized as a normal
	// teardown rather than a forfeit.
	lastEndedGuid    string
	pendingStarted   string // matchGuid to emit _MatchStarted for, "" when none
	pendingAbandoned string // matchGuid to emit _MatchAbandoned for, "" when none

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

// CurrentPhase returns just the current Phase. Convenience over
// Snapshot().Phase for emit processors that only need the gate, kept
// short so the read lock is held for the minimum.
func (m *MatchState) CurrentPhase() types.Phase {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cur.Phase
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
		} else {
			// Backfill MatchGuid when MatchCreated arrived with an empty
			// Data field (RL frequently ships it that way) and the guid
			// only landed in UpdateState. Without this, _MatchStarted
			// never fires because the guid condition stays false through
			// the entire lobby/countdown/live transition chain.
			m.backfillMatchGuid(wire.ExtractMatchGUID(evt.Raw))
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
		m.stampMatchEnded()
		m.transitionTo(types.PhaseEnded, "MatchEnded", "", true)
	case "PodiumStart":
		m.inReplay.Store(false)
		m.transitionTo(types.PhasePodium, "PodiumStart", "", true)
	case "MatchDestroyed":
		m.inReplay.Store(false)
		m.armAbandonedIfUnended()
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

// stampMatchEnded records the guid the current match ended on, so a
// subsequent MatchDestroyed can tell whether the match terminated
// cleanly. Must run before transitionTo clears any state.
func (m *MatchState) stampMatchEnded() {
	m.mu.Lock()
	if m.cur.MatchGuid != "" {
		m.lastEndedGuid = m.cur.MatchGuid
	}
	m.mu.Unlock()
}

// armAbandonedIfUnended stages a _MatchAbandoned emission when
// MatchDestroyed arrives without a matching MatchEnded for the same
// guid. RL still fires MatchDestroyed on a clean teardown — the
// guid-vs-lastEndedGuid comparison distinguishes "match over and
// torn down" from "user left mid-match". Must run before transitionTo
// clears m.cur.MatchGuid.
func (m *MatchState) armAbandonedIfUnended() {
	m.mu.Lock()
	guid := m.cur.MatchGuid
	if guid != "" && m.lastStartedGuid == guid && m.lastEndedGuid != guid {
		m.pendingAbandoned = guid
		m.emit = true
	}
	m.mu.Unlock()
}

// transitionTo unconditionally moves to the given phase. If
// keepActive is true, sets matchActive=true; if false, sets
// matchActive=false and (when guid=="clear") clears the matchGuid.
// Stages a pending _MatchState snapshot whenever the snapshot
// actually changes. Also stages a pending _MatchStarted emission
// when this is the first time a non-empty matchGuid moves into a
// live-ish phase (Countdown / Live).
func (m *MatchState) transitionTo(phase types.Phase, trigger, guid string, keepActive bool) {
	m.mu.Lock()
	prev := m.cur
	next := prev
	next.Phase = phase
	next.MatchActive = keepActive
	if !keepActive && guid == "clear" {
		next.MatchGuid = ""
		// Match torn down — let a future replay or reconnect of the
		// same guid emit _MatchStarted again.
		m.lastStartedGuid = ""
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
	if isLiveIshPhase(next.Phase) && next.MatchGuid != "" && m.lastStartedGuid != next.MatchGuid {
		m.lastStartedGuid = next.MatchGuid
		m.pendingStarted = next.MatchGuid
	}
	m.mu.Unlock()
}

// isLiveIshPhase reports whether the phase represents "match has
// actually begun" — used to gate the first-time _MatchStarted emission.
// Countdown is included so plugins resetting per-match state see the
// signal before the first goal could possibly fire.
func isLiveIshPhase(p types.Phase) bool {
	return p == types.PhaseCountdown || p == types.PhaseLive
}

// backfillMatchGuid fills in m.cur.MatchGuid when it's empty and a
// non-empty guid was discovered later (typically via UpdateState).
// RL ships MatchCreated with an empty Data string in many cases, so
// the canonical guid only arrives once UpdateState frames start. If
// _MatchStarted hasn't fired yet for this guid and the current phase
// is live-ish, this also stages the deferred emission.
func (m *MatchState) backfillMatchGuid(guid string) {
	if guid == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cur.MatchGuid != "" {
		return
	}
	m.cur.MatchGuid = guid
	m.pending.MatchGuid = guid
	if isLiveIshPhase(m.cur.Phase) && m.lastStartedGuid != guid {
		m.lastStartedGuid = guid
		m.pendingStarted = guid
		m.emit = true
	}
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
// _MatchState event if Observe set one; nil otherwise. If a
// _MatchStarted was also staged (first live-ish transition for a new
// matchGuid), it follows the _MatchState in the same batch so
// downstream emitters and plugins see them in deterministic order.
func (m *MatchState) Process(evt bus.Event) []bus.Event {
	m.mu.Lock()
	if !m.emit {
		m.mu.Unlock()
		return nil
	}
	pending := m.pending
	startedGuid := m.pendingStarted
	abandonedGuid := m.pendingAbandoned
	m.pendingStarted = ""
	m.pendingAbandoned = ""
	m.emit = false
	m.mu.Unlock()
	var out []bus.Event
	body, err := json.Marshal(pending)
	if err == nil {
		out = append(out, bus.Event{Name: "_MatchState", Data: body})
	}
	if abandonedGuid != "" {
		abBody, err := json.Marshal(struct {
			MatchGuid string `json:"matchGuid"`
		}{MatchGuid: abandonedGuid})
		if err == nil {
			out = append(out, bus.Event{Name: "_MatchAbandoned", Data: abBody})
		}
	}
	if startedGuid != "" {
		startedBody, err := json.Marshal(struct {
			MatchGuid string `json:"matchGuid"`
		}{MatchGuid: startedGuid})
		if err == nil {
			out = append(out, bus.Event{Name: "_MatchStarted", Data: startedBody})
		}
	}
	return out
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
