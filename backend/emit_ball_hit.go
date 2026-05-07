package backend

import (
	"encoding/json"
)

// BallHitEmitter republishes RL's `BallHit` event as `_BallHit` with
// resolved EnrichedPlayer references attached. Closes one of the
// wire-spec gaps: the raw BallHit ships only ShortcutRefs, which forces
// every consumer to repeat the roster-join lookup themselves.
//
// As a side effect, every BallHit also lands in the shared
// CorrelationBuffer under the "BallHit" key — the entry carries the
// resolved primary toucher plus the pre/post speeds. Downstream
// emitters that need "who hit the ball most recently" (own-goal
// detection, touch-variant statfeeds) read it back via
// CorrelationBuffer.Recent("BallHit", N) instead of decoding _BallHit
// off the bus a second time.
//
// Phase gate: liveOnly (RL fires BallHit during goal-replay cinematics
// and on the post-match screen — those aren't real touches).
type BallHitEmitter struct {
	roster      *RosterTracker
	matchState  *MatchState
	correlation *CorrelationBuffer
}

func NewBallHitEmitter(roster *RosterTracker, ms *MatchState, correlation *CorrelationBuffer) *BallHitEmitter {
	return &BallHitEmitter{roster: roster, matchState: ms, correlation: correlation}
}

func (e *BallHitEmitter) Process(evt Event) []Event {
	if evt.Name != "BallHit" {
		return nil
	}
	if e.matchState != nil {
		ph := e.matchState.Snapshot().Phase
		if ph != PhaseLive && ph != PhaseCountdown && ph != PhasePaused {
			return nil
		}
	}
	inner := unwrapInnerData(evt.Raw)
	if inner == "" {
		return nil
	}
	var d ballHitData
	if err := json.Unmarshal([]byte(inner), &d); err != nil {
		return nil
	}

	guid := d.MatchGUID
	if guid == "" {
		guid = d.MatchGUIDLow
	}
	players := d.Players
	if len(players) == 0 {
		players = d.PlayersLow
	}
	ball := d.Ball
	if ball == nil {
		ball = d.BallLow
	}

	resolved := make([]*EnrichedPlayer, 0, len(players))
	for _, p := range players {
		resolved = append(resolved, e.roster.ResolveByShortcut(p))
	}

	out := struct {
		MatchGUID    string            `json:"matchGuid,omitempty"`
		Players      []*EnrichedPlayer `json:"players"`
		PreHitSpeed  *float64          `json:"preHitSpeed,omitempty"`
		PostHitSpeed *float64          `json:"postHitSpeed,omitempty"`
		Location     *vec3             `json:"location,omitempty"`
	}{
		MatchGUID: guid,
		Players:   resolved,
	}
	if ball != nil {
		out.PreHitSpeed = ball.PreHitSpeed
		out.PostHitSpeed = ball.PostHitSpeed
		out.Location = ball.Location
	}

	// Record the touch for downstream consumers BEFORE marshaling, so
	// a marshal failure doesn't leave them looking at stale data.
	if e.correlation != nil && len(resolved) > 0 && resolved[0] != nil {
		e.correlation.Record("BallHit", &ballHitRecord{
			Player:       resolved[0],
			PreHitSpeed:  out.PreHitSpeed,
			PostHitSpeed: out.PostHitSpeed,
		})
	}

	body, err := json.Marshal(out)
	if err != nil {
		return nil
	}
	return []Event{{Name: "_BallHit", Data: body}}
}
