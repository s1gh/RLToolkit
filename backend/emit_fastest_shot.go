package main

import (
	"encoding/json"
	"sync"
)

// FastestShotEmitter publishes _FastestShotOfMatch when a new
// per-match maximum ball speed is observed across _BallHit and
// _GoalScored emissions.
//
// Decoding note: the upstream emitters (currently the legacy
// Synthesizer; eventually emit_ball_hit.go and emit_goal.go) marshal
// their payload as a flat JSON object that includes `"Event"` at the
// top level, then ship it via Bus.Broadcast(Event{Raw: …}). When that
// raw event flows back through the pipeline, evt.Data is empty —
// everything we care about lives in evt.Raw. So we decode Raw
// directly. Once the producers move to the typed Event{Name, Data}
// shape (Stage 5 producer extractions land), this can switch to
// decoding evt.Data.
type FastestShotEmitter struct {
	mu  sync.Mutex
	max float64 // negative until the first observation of the match
}

func NewFastestShotEmitter() *FastestShotEmitter {
	return &FastestShotEmitter{max: -1}
}

func (e *FastestShotEmitter) Process(evt Event) []Event {
	switch evt.Name {
	case "MatchCreated", "MatchDestroyed":
		e.mu.Lock()
		e.max = -1
		e.mu.Unlock()
		return nil
	case "_GoalScored":
		var p struct {
			GoalSpeed *float64        `json:"goalSpeed"`
			MatchGUID string          `json:"matchGuid"`
			Scorer    *EnrichedPlayer `json:"scorer"`
		}
		src := payloadBytes(evt)
		if err := json.Unmarshal(src, &p); err != nil || p.GoalSpeed == nil {
			return nil
		}
		return e.maybeEmit(*p.GoalSpeed, "GoalScored", p.Scorer, p.MatchGUID)
	case "_BallHit":
		var p struct {
			PostHitSpeed *float64          `json:"postHitSpeed"`
			MatchGUID    string            `json:"matchGuid"`
			Players      []*EnrichedPlayer `json:"players"`
		}
		src := payloadBytes(evt)
		if err := json.Unmarshal(src, &p); err != nil || p.PostHitSpeed == nil {
			return nil
		}
		var who *EnrichedPlayer
		if len(p.Players) > 0 {
			who = p.Players[0]
		}
		return e.maybeEmit(*p.PostHitSpeed, "BallHit", who, p.MatchGUID)
	}
	return nil
}

func (e *FastestShotEmitter) maybeEmit(speed float64, source string, who *EnrichedPlayer, guid string) []Event {
	if speed <= 0 {
		return nil
	}
	e.mu.Lock()
	if speed <= e.max {
		e.mu.Unlock()
		return nil
	}
	e.max = speed
	e.mu.Unlock()
	body, err := json.Marshal(struct {
		MatchGUID string          `json:"matchGuid,omitempty"`
		Speed     float64         `json:"speed"`
		Source    string          `json:"source"`
		Player    *EnrichedPlayer `json:"player,omitempty"`
	}{MatchGUID: guid, Speed: speed, Source: source, Player: who})
	if err != nil {
		return nil
	}
	return []Event{{Name: "_FastestShotOfMatch", Data: body}}
}

// payloadBytes returns the JSON object the upstream emitter produced.
// During Stage 5 the legacy Synthesizer ships its events as flat raw
// JSON (with `"Event"` at the top level and the rest of the fields
// inline), so Raw is what we want. Newer emitters return
// Event{Name, Data} where Data is the typed payload — when that
// happens, Data is non-empty and we prefer it.
func payloadBytes(evt Event) []byte {
	if len(evt.Data) > 0 {
		return evt.Data
	}
	return evt.Raw
}
