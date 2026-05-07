package emit

import (
	"encoding/json"
	"rl-toolkit/backend/internal/bus"
	"rl-toolkit/backend/internal/types"
)

// FastestShot publishes _FastestShotOfMatch when a new per-match
// maximum ball speed is observed across _BallHit and _GoalScored
// emissions.
//
// No mutex: emit processors run single-threaded inside Pipeline.Run.
// The only state — the running max — is touched only from Process,
// so there's no contention to guard.
type FastestShot struct {
	max float64 // negative until the first observation of the match
}

func NewFastestShot() *FastestShot {
	return &FastestShot{max: -1}
}

func (e *FastestShot) Process(evt bus.Event) []bus.Event {
	switch evt.Name {
	case "MatchCreated", "MatchDestroyed":
		e.max = -1
		return nil
	case "_GoalScored":
		var p struct {
			GoalSpeed *float64              `json:"goalSpeed"`
			MatchGUID string                `json:"matchGuid"`
			Scorer    *types.EnrichedPlayer `json:"scorer"`
		}
		if err := json.Unmarshal(payloadBytes(evt), &p); err != nil || p.GoalSpeed == nil {
			return nil
		}
		return e.maybeEmit(*p.GoalSpeed, "GoalScored", p.Scorer, p.MatchGUID)
	case "_BallHit":
		var p struct {
			PostHitSpeed *float64                `json:"postHitSpeed"`
			MatchGUID    string                  `json:"matchGuid"`
			Players      []*types.EnrichedPlayer `json:"players"`
		}
		if err := json.Unmarshal(payloadBytes(evt), &p); err != nil || p.PostHitSpeed == nil {
			return nil
		}
		var who *types.EnrichedPlayer
		if len(p.Players) > 0 {
			who = p.Players[0]
		}
		return e.maybeEmit(*p.PostHitSpeed, "BallHit", who, p.MatchGUID)
	}
	return nil
}

func (e *FastestShot) maybeEmit(speed float64, source string, who *types.EnrichedPlayer, guid string) []bus.Event {
	if speed <= 0 || speed <= e.max {
		return nil
	}
	e.max = speed
	body, err := json.Marshal(struct {
		MatchGUID string                `json:"matchGuid,omitempty"`
		Speed     float64               `json:"speed"`
		Source    string                `json:"source"`
		Player    *types.EnrichedPlayer `json:"player,omitempty"`
	}{MatchGUID: guid, Speed: speed, Source: source, Player: who})
	if err != nil {
		return nil
	}
	return []bus.Event{{Name: "_FastestShotOfMatch", Data: body}}
}
