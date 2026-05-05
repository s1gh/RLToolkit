package main

import (
	"bytes"
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
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

	// matchActive mirrors snap.MatchActive for lock-free reads on the
	// 60 Hz UpdateState hot path (avoids RWMutex on every tick).
	matchActive atomic.Bool

	// inReplay tracks the most recent bReplay state observed in
	// UpdateState. We can't rely on GoalReplayStart/End events — recent
	// RL builds don't emit them — so we derive replay phase by watching
	// bReplay edges on the per-tick snapshot. Atomic so the hot path
	// stays mutex-free.
	inReplay atomic.Bool

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
	// PacketSendRate. The wire ships either PascalCase ("Event":"UpdateState")
	// or all-lowercase ("event":"updatestate") depending on RL build, so we
	// check both markers.
	head := raw
	if len(head) > 96 {
		head = head[:96]
	}
	for _, m := range updateStateMarkers {
		if bytes.Contains(head, m) {
			t.onUpdateState(raw)
			return
		}
	}

	// Slow path: pull the canonical event name via the same case-tolerant
	// extractor the bus uses, then run the state machine on PascalCase
	// names regardless of wire casing. We still need Status (for
	// _ConnectionStatus) and Data (for guidFromData), so do a tolerant
	// decode for those after the name is known.
	event := extractEventName(raw)
	if event == "" {
		return
	}
	var env struct {
		Status      string          `json:"Status,omitempty"`
		StatusLower string          `json:"status,omitempty"`
		Data        json.RawMessage `json:"Data,omitempty"`
		DataLower   json.RawMessage `json:"data,omitempty"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return
	}
	status := env.Status
	if status == "" {
		status = env.StatusLower
	}
	data := env.Data
	if len(data) == 0 {
		data = env.DataLower
	}
	t.onEvent(event, status, data)
}

var updateStateMarkers = [][]byte{
	[]byte(`"Event":"UpdateState"`),
	[]byte(`"event":"updatestate"`),
}

func (t *LifecycleTracker) onUpdateState(raw []byte) {
	t.lastTickMu.Lock()
	t.lastTick = time.Now()
	t.lastTickMu.Unlock()

	// Replay-edge detection. Recent RL builds skip the discrete
	// GoalReplayStart/End events, but every UpdateState during a goal
	// replay carries bReplay=true on the Game object. Watching for
	// false→true / true→false transitions gives us reliable replay
	// phase gating regardless of which event names RL emits.
	//
	// On entering replay we set PhaseReplay only when we're in an
	// active gameplay phase. RL has been observed to keep streaming
	// UpdateStates with bReplay=true into the post-match podium /
	// "waiting for new match" screen; entering replay there would
	// stick the phase machine on "replay" forever (no UpdateState
	// arrives to flip the falling edge once the user backs out).
	//
	// On leaving replay we only revert to PhaseLive if the snapshot
	// is still PhaseReplay; CountdownBegin often fires the same tick
	// as the bReplay false-edge (post-goal kickoff) and we must not
	// clobber the countdown phase it set.
	wasReplay := t.inReplay.Load()
	nowReplay := scanBReplay(raw)
	if nowReplay != wasReplay {
		t.inReplay.Store(nowReplay)
		if nowReplay {
			t.update(func(s *LifecycleSnapshot) {
				if s.Phase == PhaseLive || s.Phase == PhaseCountdown || s.Phase == PhasePaused {
					s.Phase = PhaseReplay
				}
			})
		} else {
			t.update(func(s *LifecycleSnapshot) {
				if s.Phase == PhaseReplay {
					s.Phase = PhaseLive
				}
			})
		}
	}

	// Fast atomic check — avoids RWMutex on the 60 Hz common case.
	if t.matchActive.Load() {
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

// scanBReplay returns whether the UpdateState payload reports an active
// goal replay. The bReplay flag lives inside the JSON-encoded Data
// string, so quotes appear backslash-escaped in the raw envelope bytes
// (e.g. `\"bReplay\":true`). Casing also varies (PascalCase vs
// lowercase) by RL build. We check all four combinations; absence of
// any true-marker is treated as false. This runs on every UpdateState
// so it must stay allocation-free — four bytes.Contains over a ~1KB
// payload is still sub-microsecond.
var bReplayTrueMarkers = [][]byte{
	[]byte(`\"bReplay\":true`),
	[]byte(`\"breplay\":true`),
	[]byte(`"bReplay":true`),
	[]byte(`"breplay":true`),
}

func scanBReplay(raw []byte) bool {
	for _, m := range bReplayTrueMarkers {
		if bytes.Contains(raw, m) {
			return true
		}
	}
	return false
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
	// GoalReplayStart/GoalReplayEnd are intentionally NOT handled here.
	// They aren't reliably emitted across RL versions; replay phase is
	// derived from bReplay edges in onUpdateState instead. We still
	// pass GoalReplayWillEnd through the bus for plugins that subscribe,
	// but it doesn't drive phase.
	case "MatchEnded":
		// Drop any stale replay state — if a final-goal replay was in
		// progress when MatchEnded fired, we don't want a future
		// UpdateState's bReplay=false to retroactively flip phase to
		// PhaseLive in the wrong context.
		t.inReplay.Store(false)
		t.update(func(s *LifecycleSnapshot) { s.Phase = PhaseEnded })
	case "PodiumStart":
		t.inReplay.Store(false)
		t.update(func(s *LifecycleSnapshot) { s.Phase = PhasePodium })
	case "MatchDestroyed":
		t.inReplay.Store(false)
		t.update(func(s *LifecycleSnapshot) {
			s.MatchActive = false
			s.Phase = PhaseNone
			s.MatchGUID = ""
		})
	case "_ConnectionStatus":
		if status != "" && status != string(StatusConnected) {
			t.inReplay.Store(false)
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
	t.matchActive.Store(next.MatchActive)
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
	if !t.matchActive.Load() {
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

// extractMatchGUID pulls MatchGuid out of an UpdateState payload
// without a full Unmarshal. The field lives inside the JSON-encoded
// Data string, so quotes appear backslash-escaped in the raw envelope
// bytes. Casing also varies (PascalCase vs lowercase) by RL build. We
// try each marker in order and slice off whatever quote variant
// (escaped or plain) ends the value.
var matchGuidMarkers = [][]byte{
	[]byte(`\"MatchGuid\":\"`),
	[]byte(`\"matchguid\":\"`),
	[]byte(`"MatchGuid":"`),
	[]byte(`"matchguid":"`),
}

func extractMatchGUID(raw []byte) string {
	for _, m := range matchGuidMarkers {
		i := bytes.Index(raw, m)
		if i < 0 {
			continue
		}
		rest := raw[i+len(m):]
		// Value ends at the next quote, which may be escaped (\") if
		// we're inside a JSON-encoded string. Look for whichever
		// terminator comes first.
		end := bytes.IndexByte(rest, '"')
		if esc := bytes.Index(rest, []byte(`\"`)); esc >= 0 && (end < 0 || esc < end) {
			end = esc
		}
		if end < 0 {
			return ""
		}
		return string(rest[:end])
	}
	return ""
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
