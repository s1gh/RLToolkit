package main

import (
	"encoding/json"
	"sync"
	"time"
)

// PhaseName is the canonical phase identifier used by _LifecyclePhaseChanged.
// Distinct from LifecyclePhase (used by _Lifecycle) for backwards compatibility.
type PhaseName string

const (
	PhaseNameLobby     PhaseName = "lobby"
	PhaseNameCountdown PhaseName = "countdown"
	PhaseNameLive      PhaseName = "live"
	PhaseNamePaused    PhaseName = "paused"
	PhaseNameReplay    PhaseName = "replay"
	PhaseNamePodium    PhaseName = "podium"
	PhaseNameNone      PhaseName = "none"
)

// PhaseTransition is the payload for the _LifecyclePhaseChanged synthetic event.
type PhaseTransition struct {
	Event              string    `json:"Event"`
	MatchGUID          string    `json:"match_guid,omitempty"`
	From               PhaseName `json:"from"`
	To                 PhaseName `json:"to"`
	PhaseDurationSeconds float64 `json:"phaseDurationSeconds"`
	Trigger            string    `json:"trigger"`
}

// PhaseMachine tracks gameplay-phase edges and emits _LifecyclePhaseChanged
// whenever a transition occurs. It runs alongside the existing LifecycleTracker
// but uses a separate phase vocabulary (lobby/live/paused/replay/podium/none)
// that is richer than the legacy _Lifecycle phases.
type PhaseMachine struct {
	bus *EventBus

	// mu guards the mutable state below.
	mu sync.Mutex

	current PhaseName
	entered time.Time
	guid    string

	// preReplay is the phase we were in before entering replay, so we can
	// restore it on GoalReplayEnd.
	preReplay PhaseName
}

// NewPhaseMachine creates a phase machine tied to a bus.
func NewPhaseMachine(bus *EventBus) *PhaseMachine {
	return &PhaseMachine{
		bus:     bus,
		current: PhaseNameNone,
		entered: time.Now(),
	}
}

// Current returns the current phase. Safe for concurrent use.
func (m *PhaseMachine) Current() PhaseName {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.current
}

// Feed is called from the RL client's dispatcher with each raw packet.
// It watches for lifecycle-driving events and updates the phase machine.
func (m *PhaseMachine) Feed(raw []byte) {
	event := extractEventName(raw)
	if event == "" {
		return
	}

	// Extract MatchGuid when present so transitions carry context.
	var env struct {
		Data      string `json:"Data"`
		DataLower string `json:"data"`
	}
	_ = json.Unmarshal(raw, &env)
	data := env.Data
	if data == "" {
		data = env.DataLower
	}
	guid := guidFromData(json.RawMessage(data))

	switch event {
	case "MatchCreated":
		m.transition(PhaseNameLobby, "MatchCreated", guid)
	case "CountdownBegin":
		m.transition(PhaseNameCountdown, "CountdownBegin", guid)
	case "RoundStarted":
		m.transition(PhaseNameLive, "RoundStarted", guid)
	case "MatchPaused":
		m.transition(PhaseNamePaused, "MatchPaused", guid)
	case "MatchUnpaused":
		m.transition(PhaseNameLive, "MatchUnpaused", guid)
	case "GoalReplayStart":
		m.enterReplay("GoalReplayStart", guid)
	case "GoalReplayEnd":
		m.exitReplay("GoalReplayEnd", guid)
	case "MatchEnded":
		// MatchEnded does NOT change the phase — gameplay continues in
		// live (or paused/replay) until PodiumStart. This matches the
		// plan's spec: "continues live until PodiumStart".
	case "PodiumStart":
		m.transition(PhaseNamePodium, "PodiumStart", guid)
	case "MatchDestroyed":
		m.transition(PhaseNameNone, "MatchDestroyed", "")
	}
}

// transition moves to a new phase and emits _LifecyclePhaseChanged if
// the phase actually changed.
func (m *PhaseMachine) transition(to PhaseName, trigger, guid string) {
	m.mu.Lock()
	from := m.current
	if to == from {
		m.mu.Unlock()
		return
	}
	dur := time.Since(m.entered).Seconds()
	m.current = to
	m.entered = time.Now()
	if guid != "" {
		m.guid = guid
	}
	// On leaving replay, clear the saved pre-replay phase.
	if from == PhaseNameReplay {
		m.preReplay = PhaseNameNone
	}
	g := m.guid
	m.mu.Unlock()

	m.emit(from, to, dur, trigger, g)
}

// enterReplay saves the current phase (so we can restore it) and moves
// to replay.
func (m *PhaseMachine) enterReplay(trigger, guid string) {
	m.mu.Lock()
	if m.current == PhaseNameReplay {
		m.mu.Unlock()
		return
	}
	m.preReplay = m.current
	from := m.current
	dur := time.Since(m.entered).Seconds()
	m.current = PhaseNameReplay
	m.entered = time.Now()
	if guid != "" {
		m.guid = guid
	}
	g := m.guid
	m.mu.Unlock()

	m.emit(from, PhaseNameReplay, dur, trigger, g)
}

// exitReplay restores the phase we were in before replay.
func (m *PhaseMachine) exitReplay(trigger, guid string) {
	m.mu.Lock()
	if m.current != PhaseNameReplay {
		m.mu.Unlock()
		return
	}
	to := m.preReplay
	if to == PhaseNameNone {
		// Fallback: if we don't know what we were in before replay,
		// assume live (the safest default for post-goal gameplay).
		to = PhaseNameLive
	}
	from := m.current
	dur := time.Since(m.entered).Seconds()
	m.current = to
	m.entered = time.Now()
	m.preReplay = PhaseNameNone
	if guid != "" {
		m.guid = guid
	}
	g := m.guid
	m.mu.Unlock()

	m.emit(from, to, dur, trigger, g)
}

func (m *PhaseMachine) emit(from, to PhaseName, dur float64, trigger, guid string) {
	b, err := json.Marshal(PhaseTransition{
		Event:                "_LifecyclePhaseChanged",
		MatchGUID:            guid,
		From:                 from,
		To:                   to,
		PhaseDurationSeconds: dur,
		Trigger:              trigger,
	})
	if err != nil {
		return
	}
	m.bus.Publish(b)
}
