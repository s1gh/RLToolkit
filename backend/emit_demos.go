package backend

import (
	"encoding/json"
	"sync"
	"time"
)

// demoChainEntry pairs a demo timestamp with the resolved victim so
// _DemoChain can report `victims[]` for the chain so far.
type demoChainEntry struct {
	at     time.Time
	victim *EnrichedPlayer
}

// demoChainWindow is the sliding window within which back-to-back
// demos by the same attacker count as a chain. 5s is wide enough to
// cover a player chasing across the field, narrow enough that the
// next match's first demo doesn't extend a stale chain.
const demoChainWindow = 5 * time.Second

// DemosEmitter consumes the typed `_Demolish` events StatfeedEmitter
// publishes and emits the richer wire-facing pair:
//
//   - `_PlayerDemolished` with attackerSpeed / attackerWasSupersonic
//     (pulled from TickStore for SPECTATOR-only ticks) and the
//     isSelfDemo / isTeamDemo flags.
//   - `_DemoChain` when the same attacker has registered ≥2 demos
//     within demoChainWindow.
//
// State owned:
//
//   - demolishLog: raw _PlayerDemolished envelopes (wire-shaped) for
//     SSE replay.
//   - demoChainByID: per-attacker rolling demo timestamps.
//
// Both reset on MatchCreated / MatchDestroyed.
type DemosEmitter struct {
	ticks *TickStore

	demolishMu  sync.Mutex
	demolishLog [][]byte

	chainMu       sync.Mutex
	demoChainByID map[string][]demoChainEntry
}

func NewDemosEmitter(ticks *TickStore) *DemosEmitter {
	return &DemosEmitter{
		ticks:         ticks,
		demoChainByID: make(map[string][]demoChainEntry),
	}
}

// DemolishLog returns a snapshot of every _PlayerDemolished envelope
// published since the current match started, in wire-shape (ready
// for an SSE handler to write directly).
func (e *DemosEmitter) DemolishLog() [][]byte {
	e.demolishMu.Lock()
	defer e.demolishMu.Unlock()
	if len(e.demolishLog) == 0 {
		return nil
	}
	out := make([][]byte, len(e.demolishLog))
	copy(out, e.demolishLog)
	return out
}

func (e *DemosEmitter) Process(evt Event) []Event {
	switch evt.Name {
	case "MatchCreated", "MatchDestroyed":
		e.reset()
		return nil
	case "_Demolish":
		return e.processDemolish(evt)
	}
	return nil
}

func (e *DemosEmitter) reset() {
	e.demolishMu.Lock()
	e.demolishLog = nil
	e.demolishMu.Unlock()

	e.chainMu.Lock()
	for k := range e.demoChainByID {
		delete(e.demoChainByID, k)
	}
	e.chainMu.Unlock()
}

func (e *DemosEmitter) processDemolish(evt Event) []Event {
	var d struct {
		MatchGUID string          `json:"matchGuid"`
		Attacker  *EnrichedPlayer `json:"attacker"`
		Victim    *EnrichedPlayer `json:"victim"`
	}
	if err := json.Unmarshal(payloadBytes(evt), &d); err != nil {
		return nil
	}
	if d.Attacker == nil || d.Victim == nil {
		return nil
	}

	attackerSpeed, attackerSupersonic := e.ticks.PlayerScalars(d.Attacker.ID)
	payload := struct {
		MatchGUID             string          `json:"matchGuid,omitempty"`
		Attacker              *EnrichedPlayer `json:"attacker"`
		Victim                *EnrichedPlayer `json:"victim"`
		IsSelfDemo            bool            `json:"isSelfDemo,omitempty"`
		IsTeamDemo            bool            `json:"isTeamDemo,omitempty"`
		AttackerSpeed         *float64        `json:"attackerSpeed,omitempty"`
		AttackerWasSupersonic *bool           `json:"attackerWasSupersonic,omitempty"`
	}{
		MatchGUID:     d.MatchGUID,
		Attacker:      d.Attacker,
		Victim:        d.Victim,
		AttackerSpeed: attackerSpeed,
	}
	if attackerSpeed != nil {
		ss := attackerSupersonic
		payload.AttackerWasSupersonic = &ss
	}
	if d.Attacker.ID != "" && d.Attacker.ID == d.Victim.ID {
		payload.IsSelfDemo = true
	} else if d.Attacker.Team == d.Victim.Team {
		payload.IsTeamDemo = true
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil
	}
	out := []Event{{Name: "_PlayerDemolished", Data: body}}

	// Build the wire-shaped envelope for SSE replay (the bus
	// envelope it would produce). Done once here so the SSE handler
	// can write it directly.
	envelope, err := json.Marshal(struct {
		Event string          `json:"Event"`
		Data  json.RawMessage `json:"Data"`
	}{Event: "_PlayerDemolished", Data: body})
	if err == nil {
		e.demolishMu.Lock()
		e.demolishLog = append(e.demolishLog, envelope)
		e.demolishMu.Unlock()
	}

	// Demo-chain detection. Self-demos and team-demos still count
	// as actions but skip chain reporting (a chain over your own
	// teammate isn't a hype moment) — and are kept out of the
	// per-attacker history so they don't extend chains over real
	// demos.
	if !payload.IsSelfDemo && !payload.IsTeamDemo && d.Attacker.ID != "" {
		if chain := e.recordChain(d.MatchGUID, d.Attacker, d.Victim); chain != nil {
			out = append(out, *chain)
		}
	}
	return out
}

func (e *DemosEmitter) recordChain(guid string, attacker, victim *EnrichedPlayer) *Event {
	now := time.Now()
	cutoff := now.Add(-demoChainWindow)
	e.chainMu.Lock()
	hist := e.demoChainByID[attacker.ID]
	trimmed := hist[:0]
	for _, h := range hist {
		if h.at.After(cutoff) {
			trimmed = append(trimmed, h)
		}
	}
	trimmed = append(trimmed, demoChainEntry{at: now, victim: victim})
	e.demoChainByID[attacker.ID] = trimmed
	count := len(trimmed)
	victims := make([]*EnrichedPlayer, 0, count)
	for _, h := range trimmed {
		victims = append(victims, h.victim)
	}
	windowStart := trimmed[0].at
	e.chainMu.Unlock()

	if count < 2 {
		return nil
	}
	body, err := json.Marshal(struct {
		MatchGUID     string            `json:"matchGuid,omitempty"`
		Attacker      *EnrichedPlayer   `json:"attacker"`
		Victims       []*EnrichedPlayer `json:"victims"`
		Count         int               `json:"count"`
		WindowSeconds float64           `json:"windowSeconds"`
	}{
		MatchGUID:     guid,
		Attacker:      attacker,
		Victims:       victims,
		Count:         count,
		WindowSeconds: now.Sub(windowStart).Seconds(),
	})
	if err != nil {
		return nil
	}
	return &Event{Name: "_DemoChain", Data: body}
}
