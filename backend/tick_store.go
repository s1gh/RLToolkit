package main

import (
	"encoding/json"
	"sync"
)

// TickStore caches the most recent UpdateState envelope decoded into
// the slim tickSnapshot shape. One instance is shared across every
// processor that needs per-tick player/team scalars (the goal/own-goal/
// touch-variant emitters, the diff emitters, the synth's
// _MatchSummary writer, etc.).
//
// Owns the parse so consumers don't each repeat the JSON decode at
// 60-120Hz. Consumers read via the Latest / Previous accessors —
// they're cheap (a single read lock + struct return).
type TickStore struct {
	mu       sync.RWMutex
	previous *tickSnapshot
	latest   *tickSnapshot
}

func NewTickStore() *TickStore { return &TickStore{} }

// Observe decodes an UpdateState envelope, advances (previous, latest),
// and returns. Non-UpdateState events are skipped.
//
// Implements StateProcessor so the pipeline can register it directly.
func (t *TickStore) Observe(evt Event) {
	if evt.Name != "UpdateState" {
		return
	}
	inner := unwrapInnerData(evt.Raw)
	if inner == "" {
		return
	}
	var d updateStateFull
	if err := json.Unmarshal([]byte(inner), &d); err != nil {
		return
	}
	g := d.Game
	if g == nil {
		g = d.GameLow
	}
	if g == nil || len(g.Teams) == 0 {
		return
	}

	guid := pickStr(d.MatchGUID, d.MatchGUIDLow)
	teams := make([]teamRef, 0, len(g.Teams))
	for _, t := range g.Teams {
		teams = append(teams, teamRef{
			TeamNum:        t.TeamNum,
			Name:           t.Name,
			Score:          t.Score,
			ColorPrimary:   t.ColorPrimary,
			ColorSecondary: t.ColorSecondary,
		})
	}

	wirePlayers := d.Players
	if len(wirePlayers) == 0 {
		wirePlayers = d.PlayersLow
	}
	players := make([]tickPlayer, 0, len(wirePlayers))
	for _, p := range wirePlayers {
		var boost *int
		if p.Boost != nil {
			b := *p.Boost
			boost = &b
		}
		var speed *float64
		if p.Speed != nil {
			sp := *p.Speed
			speed = &sp
		}
		players = append(players, tickPlayer{
			id:         canonicalizeBotId(p.PrimaryID, p.Name),
			name:       p.Name,
			team:       p.TeamNum,
			score:      p.Score,
			goals:      p.Goals,
			assists:    p.Assists,
			saves:      p.Saves,
			shots:      p.Shots,
			touches:    p.Touches,
			carTouches: p.CarTouches,
			demos:      p.Demos,
			boost:      boost,
			demolished: p.BDemolished,
			onGround:   p.BOnGround,
			speed:      speed,
			supersonic: p.BSupersonic,
		})
	}

	curr := &tickSnapshot{
		matchGUID: guid,
		teams:     teams,
		players:   players,
		overtime:  g.BOvertime,
		bReplay:   g.BReplay,
	}
	if g.Ball != nil {
		curr.ballTeam = g.Ball.TeamNum
		curr.hasBall = true
	}

	t.mu.Lock()
	t.previous = t.latest
	t.latest = curr
	t.mu.Unlock()
}

// Latest returns the most recently observed tickSnapshot, or nil if no
// UpdateState has arrived yet.
func (t *TickStore) Latest() *tickSnapshot {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.latest
}

// Previous returns the snapshot just before Latest, or nil on the
// first tick. Used by diff emitters.
func (t *TickStore) Previous() *tickSnapshot {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.previous
}

// TeamByNum returns a copy of the team with the given TeamNum from
// the most recent tick, or nil if no UpdateState has arrived or the
// team isn't present.
func (t *TickStore) TeamByNum(num int) *teamRef {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.latest == nil {
		return nil
	}
	for i := range t.latest.teams {
		if t.latest.teams[i].TeamNum == num {
			team := t.latest.teams[i]
			return &team
		}
	}
	return nil
}

// PlayerScalars returns Speed and bSupersonic for the player with the
// given canonical id from the latest tick. The first return is nil
// when the player is missing or RL didn't ship Speed (non-spectator
// builds omit it).
func (t *TickStore) PlayerScalars(id string) (*float64, bool) {
	if id == "" {
		return nil, false
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.latest == nil {
		return nil, false
	}
	for i := range t.latest.players {
		p := &t.latest.players[i]
		if p.id != id {
			continue
		}
		if p.speed == nil {
			return nil, false
		}
		sp := *p.speed
		return &sp, p.supersonic
	}
	return nil, false
}

// InReplay reports whether the most recent UpdateState had bReplay
// set. Used by emitters that gate on the replay edge but aren't the
// processor that published _GoalReplayStarted.
func (t *TickStore) InReplay() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.latest != nil && t.latest.bReplay
}
