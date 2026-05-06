package main

import (
	"encoding/json"
)

// BallHitEmitter republishes RL's `BallHit` event as `_BallHit` with
// resolved EnrichedPlayer references attached. Closes one of the
// wire-spec gaps: the raw BallHit ships only ShortcutRefs, which forces
// every consumer to repeat the roster-join lookup themselves.
//
// Phase gate: liveOnly (RL fires BallHit during goal-replay cinematics
// and on the post-match screen — those aren't real touches).
type BallHitEmitter struct {
	roster     *RosterTracker
	matchState *MatchState
}

func NewBallHitEmitter(roster *RosterTracker, ms *MatchState) *BallHitEmitter {
	return &BallHitEmitter{roster: roster, matchState: ms}
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
	body, err := json.Marshal(out)
	if err != nil {
		return nil
	}
	return []Event{{Name: "_BallHit", Data: body}}
}
