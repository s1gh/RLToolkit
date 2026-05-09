package emit

import (
	"encoding/json"
	"rl-toolkit/backend/internal/bus"
	"rl-toolkit/backend/internal/types"
	"rl-toolkit/backend/internal/wire"
)

// BallHit republishes RL's `BallHit` as `_BallHit` with resolved
// EnrichedPlayer references attached, sparing every consumer the
// roster-join lookup.
//
// Side effect: every BallHit lands in the shared Correlator under the
// "BallHit" key (resolved primary toucher + pre/post speeds), so
// downstream emitters can ask "who hit it most recently?" via
// Recent("BallHit", N) without re-decoding off the bus.
//
// Phase-gated to live gameplay — RL also fires BallHit during goal-
// replay cinematics and the post-match screen, which aren't real touches.
type BallHit struct {
	roster      RosterResolver
	phase       PhaseGate
	correlation Correlator
}

func NewBallHit(roster RosterResolver, phase PhaseGate, correlation Correlator) *BallHit {
	return &BallHit{roster: roster, phase: phase, correlation: correlation}
}

func (e *BallHit) Process(evt bus.Event) []bus.Event {
	if evt.Name != "BallHit" {
		return nil
	}
	if !liveGameplayPhases(e.phase) {
		return nil
	}
	inner := wire.UnwrapInnerData(evt.Raw)
	if inner == "" {
		return nil
	}
	var d types.BallHitData
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

	resolved := make([]*types.EnrichedPlayer, 0, len(players))
	for _, p := range players {
		resolved = append(resolved, e.roster.ResolveByShortcut(p))
	}

	out := struct {
		MatchGUID    string                  `json:"matchGuid,omitempty"`
		Players      []*types.EnrichedPlayer `json:"players"`
		PreHitSpeed  *float64                `json:"preHitSpeed,omitempty"`
		PostHitSpeed *float64                `json:"postHitSpeed,omitempty"`
		Location     *types.Vec3             `json:"location,omitempty"`
	}{
		MatchGUID: guid,
		Players:   resolved,
	}
	if ball != nil {
		out.PreHitSpeed = ball.PreHitSpeed
		out.PostHitSpeed = ball.PostHitSpeed
		out.Location = ball.Location
	}

	// Record before marshaling so a marshal failure doesn't leave
	// downstream consumers looking at stale data.
	if e.correlation != nil && len(resolved) > 0 && resolved[0] != nil {
		e.correlation.Record("BallHit", &types.BallHitRecord{
			Player:       resolved[0],
			PreHitSpeed:  out.PreHitSpeed,
			PostHitSpeed: out.PostHitSpeed,
		})
	}

	body, err := json.Marshal(out)
	if err != nil {
		return nil
	}
	return []bus.Event{{Name: "_BallHit", Data: body}}
}
