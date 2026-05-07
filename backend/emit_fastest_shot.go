package backend

import (
	"encoding/json"
	"rl-toolkit/internal/bus"
)

// FastestShotEmitter publishes _FastestShotOfMatch when a new
// per-match maximum ball speed is observed across _BallHit and
// _GoalScored emissions.
//
// No mutex: emit processors run single-threaded inside Pipeline.Run.
// The only state — the running max — is touched only from Process,
// so there's no contention to guard.
//
// Decoding note: while the legacy Synthesizer is still the producer,
// _GoalScored and _BallHit arrive with everything inline in evt.Raw
// and evt.Data is empty. payloadBytes prefers Data when populated so
// once Tasks 5.3 / 5.9 land, no edit is needed here.
type FastestShotEmitter struct {
	max float64 // negative until the first observation of the match
}

func NewFastestShotEmitter() *FastestShotEmitter {
	return &FastestShotEmitter{max: -1}
}

func (e *FastestShotEmitter) Process(evt bus.Event) []bus.Event {
	switch evt.Name {
	case "MatchCreated", "MatchDestroyed":
		e.max = -1
		return nil
	case "_GoalScored":
		var p struct {
			GoalSpeed *float64        `json:"goalSpeed"`
			MatchGUID string          `json:"matchGuid"`
			Scorer    *EnrichedPlayer `json:"scorer"`
		}
		if err := json.Unmarshal(payloadBytes(evt), &p); err != nil || p.GoalSpeed == nil {
			return nil
		}
		return e.maybeEmit(*p.GoalSpeed, "GoalScored", p.Scorer, p.MatchGUID)
	case "_BallHit":
		var p struct {
			PostHitSpeed *float64          `json:"postHitSpeed"`
			MatchGUID    string            `json:"matchGuid"`
			Players      []*EnrichedPlayer `json:"players"`
		}
		if err := json.Unmarshal(payloadBytes(evt), &p); err != nil || p.PostHitSpeed == nil {
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

func (e *FastestShotEmitter) maybeEmit(speed float64, source string, who *EnrichedPlayer, guid string) []bus.Event {
	if speed <= 0 || speed <= e.max {
		return nil
	}
	e.max = speed
	body, err := json.Marshal(struct {
		MatchGUID string          `json:"matchGuid,omitempty"`
		Speed     float64         `json:"speed"`
		Source    string          `json:"source"`
		Player    *EnrichedPlayer `json:"player,omitempty"`
	}{MatchGUID: guid, Speed: speed, Source: source, Player: who})
	if err != nil {
		return nil
	}
	return []bus.Event{{Name: "_FastestShotOfMatch", Data: body}}
}

// payloadBytes returns the JSON object the upstream emitter produced.
// During the synth-bridged transition the legacy producer ships flat
// raw JSON (with "Event" at the top level inline with the fields), so
// Raw is what we want. Native emit processors return Event{Name, Data}
// where Data is the typed payload — when that's set we prefer it.
func payloadBytes(evt bus.Event) []byte {
	if len(evt.Data) > 0 {
		return evt.Data
	}
	return evt.Raw
}
