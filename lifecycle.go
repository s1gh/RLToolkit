package main

import (
	"bytes"
	"context"
	"encoding/json"
	"sync"
	"time"
)

// LifecyclePhase mirrors the gameplay-phase enum the SDK has tracked
// client-side until now. Same names so existing plugin code (whilePhase,
// onLifecycle) keeps working — the values are just authoritative now,
// produced by the toolkit instead of inferred per-client.
type LifecyclePhase string

const (
	PhaseNone      LifecyclePhase = "none"
	PhaseCreated   LifecyclePhase = "created"
	PhaseCountdown LifecyclePhase = "countdown"
	PhaseLive      LifecyclePhase = "live"
	PhasePaused    LifecyclePhase = "paused"
	PhaseReplay    LifecyclePhase = "replay"
	PhaseEnded     LifecyclePhase = "ended"
	PhasePodium    LifecyclePhase = "podium"
)

// LifecycleSnapshot is the wire format both for the SSE _Lifecycle
// event and the /api/lifecycle endpoint.
//
// match_active and phase carry distinct signals. "Am I in a match" is
// match_active — the right question for "should the widget be visible".
// "What's the gameplay phase" is phase — the right question for
// whilePhase gating. Conflating them is what made the SDK's previous
// machine miss the "back out to menu without MatchDestroyed" case.
type LifecycleSnapshot struct {
	MatchActive bool           `json:"match_active"`
	Phase       LifecyclePhase `json:"phase"`
	MatchGUID   string         `json:"match_guid,omitempty"`
	Since       time.Time      `json:"since"`
}

// matchActiveTimeout is how long we tolerate silence on the UpdateState
// stream before declaring match_active=false. RL goes briefly silent
// during arena loads (~1s); 5s is safely above that floor and still
// catches "user backed out to menu" within a few ticks.
const matchActiveTimeout = 5 * time.Second

// LifecycleTracker observes raw RL events flowing through the bus and
// maintains an authoritative gameplay-state snapshot. State transitions
// publish a synthetic "_Lifecycle" SSE event back through the same bus
// so SDK clients see them inline with everything else.
type LifecycleTracker struct {
	bus     *EventBus
	timeout time.Duration

	mu   sync.RWMutex
	snap LifecycleSnapshot

	lastTickMu sync.Mutex
	lastTick   time.Time
}

// NewLifecycleTracker creates a tracker tied to a bus. Callers must
// invoke Run on the returned tracker for the watchdog to start.
func NewLifecycleTracker(bus *EventBus) *LifecycleTracker {
	return &LifecycleTracker{
		bus:     bus,
		timeout: matchActiveTimeout,
		snap: LifecycleSnapshot{
			MatchActive: false,
			Phase:       PhaseNone,
			Since:       time.Now(),
		},
	}
}

// Snapshot returns the current lifecycle state. Safe for concurrent use.
func (t *LifecycleTracker) Snapshot() LifecycleSnapshot {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.snap
}

// Feed is called from the RL client's dispatcher with each raw packet,
// before it's published on the bus. We peek at the Event name and run
// the state machine; full payloads are decoded only when relevant.
//
// Cheap by construction: no JSON decode on the 60Hz UpdateState path
// (substring match instead), no allocations on that hot path.
func (t *LifecycleTracker) Feed(raw []byte) {
	// Hot path: UpdateState. Substring-detect to avoid json.Unmarshal at
	// PacketSendRate.
	head := raw
	if len(head) > 96 {
		head = head[:96]
	}
	if bytes.Contains(head, updateStateMarker) {
		t.onUpdateState(raw)
		return
	}

	// Slow path: decode the envelope so we can switch on Event.
	var env struct {
		Event  string          `json:"Event"`
		Status string          `json:"Status,omitempty"` // _ConnectionStatus only
		Data   json.RawMessage `json:"Data,omitempty"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return
	}
	if env.Event == "" {
		return
	}
	t.onEvent(env.Event, env.Status, env.Data)
}

var updateStateMarker = []byte(`"Event":"UpdateState"`)

func (t *LifecycleTracker) onUpdateState(raw []byte) {
	t.lastTickMu.Lock()
	t.lastTick = time.Now()
	t.lastTickMu.Unlock()

	// If we'd previously declared match_inactive (after a timeout, say,
	// or before any RL events arrived), an UpdateState resurrects us.
	if t.Snapshot().MatchActive {
		return
	}
	guid := extractMatchGUID(raw)
	t.update(func(s *LifecycleSnapshot) {
		s.MatchActive = true
		s.MatchGUID = guid
		// Best-effort initial phase: if we're booting mid-match without
		// any prior lifecycle events, "live" is the safe default. Real
		// transitions follow as RL emits its lifecycle events.
		if s.Phase == PhaseNone {
			s.Phase = PhaseLive
		}
	})
}

// onEvent is the lifecycle state machine for non-UpdateState events.
//
// Mirrors the SDK's previous machine but with two differences:
//   - PhaseNone takes the role the SDK called `idle` when match_active
//     is false.
//   - match_active is tracked independently so plugins can ask "am I in
//     a match" without inferring it from phase transitions.
func (t *LifecycleTracker) onEvent(event, status string, data json.RawMessage) {
	switch event {
	case "MatchCreated":
		t.update(func(s *LifecycleSnapshot) {
			s.MatchActive = true
			s.Phase = PhaseCreated
			s.MatchGUID = guidFromData(data)
		})
	case "MatchInitialized":
		t.update(func(s *LifecycleSnapshot) {
			s.MatchActive = true
			s.Phase = PhaseCountdown
		})
	case "CountdownBegin":
		t.update(func(s *LifecycleSnapshot) { s.Phase = PhaseCountdown })
	case "RoundStarted":
		t.update(func(s *LifecycleSnapshot) { s.Phase = PhaseLive })
	case "MatchPaused":
		t.update(func(s *LifecycleSnapshot) { s.Phase = PhasePaused })
	case "MatchUnpaused":
		// Best-effort: assume back to live play. RL doesn't tell us what
		// phase we paused from.
		t.update(func(s *LifecycleSnapshot) { s.Phase = PhaseLive })
	case "GoalReplayStart":
		t.update(func(s *LifecycleSnapshot) { s.Phase = PhaseReplay })
	case "GoalReplayEnd":
		t.update(func(s *LifecycleSnapshot) { s.Phase = PhaseLive })
	case "MatchEnded":
		t.update(func(s *LifecycleSnapshot) { s.Phase = PhaseEnded })
	case "PodiumStart":
		t.update(func(s *LifecycleSnapshot) { s.Phase = PhasePodium })
	case "MatchDestroyed":
		t.update(func(s *LifecycleSnapshot) {
			s.MatchActive = false
			s.Phase = PhaseNone
			s.MatchGUID = ""
		})
	case "_ConnectionStatus":
		if status != "" && status != string(StatusConnected) {
			t.update(func(s *LifecycleSnapshot) {
				s.MatchActive = false
				s.Phase = PhaseNone
				s.MatchGUID = ""
			})
		}
	}
}

// update mutates the snapshot under lock, sets Since on real changes,
// and publishes a synthetic _Lifecycle event whenever the snapshot
// actually changed. No-op on identity transitions.
func (t *LifecycleTracker) update(mutate func(*LifecycleSnapshot)) {
	t.mu.Lock()
	prev := t.snap
	next := prev
	mutate(&next)
	if next.MatchActive == prev.MatchActive &&
		next.Phase == prev.Phase &&
		next.MatchGUID == prev.MatchGUID {
		t.mu.Unlock()
		return
	}
	next.Since = time.Now()
	t.snap = next
	t.mu.Unlock()

	t.publish(next)
}

// lifecycleEvent is the SSE wire shape. The "_" prefix marks it as a
// toolkit-synthesized event (same convention as _ConnectionStatus) so
// SDK clients can route it before the typed-event normalizer runs.
type lifecycleEvent struct {
	Event       string         `json:"Event"`
	MatchActive bool           `json:"match_active"`
	Phase       LifecyclePhase `json:"phase"`
	MatchGUID   string         `json:"match_guid,omitempty"`
	Since       time.Time      `json:"since"`
}

func (t *LifecycleTracker) publish(s LifecycleSnapshot) {
	b, err := json.Marshal(lifecycleEvent{
		Event:       "_Lifecycle",
		MatchActive: s.MatchActive,
		Phase:       s.Phase,
		MatchGUID:   s.MatchGUID,
		Since:       s.Since,
	})
	if err != nil {
		return
	}
	t.bus.Publish(b)
}

// Run starts the watchdog goroutine that flips match_active=false when
// no UpdateState has arrived within the configured timeout. Blocks
// until ctx is cancelled.
func (t *LifecycleTracker) Run(ctx context.Context) {
	tick := time.NewTicker(t.timeout / 2)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			t.checkTimeout()
		}
	}
}

func (t *LifecycleTracker) checkTimeout() {
	if !t.Snapshot().MatchActive {
		return
	}
	t.lastTickMu.Lock()
	last := t.lastTick
	t.lastTickMu.Unlock()
	if last.IsZero() || time.Since(last) < t.timeout {
		return
	}
	// RL went silent. Covers the "user backed out to menu" case where
	// RL doesn't emit MatchDestroyed.
	t.update(func(s *LifecycleSnapshot) {
		s.MatchActive = false
		s.Phase = PhaseNone
		s.MatchGUID = ""
	})
}

// extractMatchGUID pulls MatchGuid out of an UpdateState payload without
// a full Unmarshal. The field is small (UUID, ~36 bytes) and reliably
// near the start of the JSON, so a substring scan is dramatically
// cheaper than json.Unmarshal at 60Hz.
func extractMatchGUID(raw []byte) string {
	const key = `"MatchGuid":"`
	i := bytes.Index(raw, []byte(key))
	if i < 0 {
		return ""
	}
	rest := raw[i+len(key):]
	end := bytes.IndexByte(rest, '"')
	if end < 0 {
		return ""
	}
	return string(rest[:end])
}

// guidFromData reads MatchGuid from a typed event's Data payload. RL
// ships Data as a JSON-encoded *string* containing JSON, hence the
// double decode.
func guidFromData(data json.RawMessage) string {
	if len(data) == 0 {
		return ""
	}
	var inner string
	if err := json.Unmarshal(data, &inner); err != nil {
		return ""
	}
	return extractMatchGUID([]byte(inner))
}
