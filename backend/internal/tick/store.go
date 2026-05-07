// Package tick caches the most recent UpdateState envelope decoded
// into the slim TickSnapshot shape. One Store is shared across every
// processor that needs per-tick player/team scalars (the goal/own-goal/
// touch-variant emitters, the diff emitters, the synth's
// _MatchSummary writer, etc.).
//
// Owns the parse so consumers don't each repeat the JSON decode at
// 60-120Hz. Consumers read via the Latest / Previous accessors —
// they're cheap (a single read lock + struct return).
package tick

import (
	"encoding/json"
	"rl-toolkit/backend/internal/bus"
	"rl-toolkit/backend/internal/types"
	"rl-toolkit/backend/internal/wire"
	"sync"
)

// Store is the shared tick cache.
type Store struct {
	mu       sync.RWMutex
	previous *types.TickSnapshot
	latest   *types.TickSnapshot
}

func New() *Store { return &Store{} }

// updateStateFull mirrors the wire shape we need for tick diffs.
// Same case-tolerant pattern as the rest of the toolkit: accept
// PascalCase or lowercase top-level keys.
type updateStateFull struct {
	MatchGUID    string                  `json:"MatchGuid"`
	MatchGUIDLow string                  `json:"matchguid"`
	Game         *updateStateGame        `json:"Game"`
	GameLow      *updateStateGame        `json:"game"`
	Players      []updateStateFullPlayer `json:"Players"`
	PlayersLow   []updateStateFullPlayer `json:"players"`
}

type updateStateGame struct {
	Teams []types.WireTeam `json:"Teams"`
	Ball  *struct {
		TeamNum int `json:"TeamNum"`
	} `json:"Ball"`
	BOvertime bool `json:"bOvertime"`
	BReplay   bool `json:"bReplay"`
}

type updateStateFullPlayer struct {
	PrimaryID   string   `json:"PrimaryId"`
	Name        string   `json:"Name"`
	TeamNum     int      `json:"TeamNum"`
	Score       int      `json:"Score"`
	Goals       int      `json:"Goals"`
	Assists     int      `json:"Assists"`
	Saves       int      `json:"Saves"`
	Shots       int      `json:"Shots"`
	Touches     int      `json:"Touches"`
	CarTouches  int      `json:"CarTouches"`
	Demos       int      `json:"Demos"`
	Boost       *int     `json:"Boost"`
	Speed       *float64 `json:"Speed"`
	BDemolished bool     `json:"bDemolished"`
	BOnGround   bool     `json:"bOnGround"`
	BSupersonic bool     `json:"bSupersonic"`
}

// Observe decodes an UpdateState envelope, advances (previous, latest),
// and returns. Non-UpdateState events are skipped. Implements the
// pipeline's StateProcessor interface structurally.
func (t *Store) Observe(evt bus.Event) {
	if evt.Name != "UpdateState" {
		return
	}
	inner := wire.UnwrapInnerData(evt.Raw)
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

	guid := wire.PickStr(d.MatchGUID, d.MatchGUIDLow)
	teams := make([]types.TeamRef, 0, len(g.Teams))
	for _, t := range g.Teams {
		teams = append(teams, types.TeamRef{
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
	players := make([]types.TickPlayer, 0, len(wirePlayers))
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
		players = append(players, types.TickPlayer{
			ID:         types.CanonicalizeBotID(p.PrimaryID, p.Name),
			Name:       p.Name,
			Team:       p.TeamNum,
			Score:      p.Score,
			Goals:      p.Goals,
			Assists:    p.Assists,
			Saves:      p.Saves,
			Shots:      p.Shots,
			Touches:    p.Touches,
			CarTouches: p.CarTouches,
			Demos:      p.Demos,
			Boost:      boost,
			Demolished: p.BDemolished,
			OnGround:   p.BOnGround,
			Speed:      speed,
			Supersonic: p.BSupersonic,
		})
	}

	curr := &types.TickSnapshot{
		MatchGUID: guid,
		Teams:     teams,
		Players:   players,
		Overtime:  g.BOvertime,
		BReplay:   g.BReplay,
	}
	if g.Ball != nil {
		curr.BallTeam = g.Ball.TeamNum
		curr.HasBall = true
	}

	t.mu.Lock()
	t.previous = t.latest
	t.latest = curr
	t.mu.Unlock()
}

// Latest returns the most recently observed snapshot, or nil if no
// UpdateState has arrived yet.
func (t *Store) Latest() *types.TickSnapshot {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.latest
}

// Previous returns the snapshot just before Latest, or nil on the
// first tick. Used by diff emitters.
func (t *Store) Previous() *types.TickSnapshot {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.previous
}

// TeamByNum returns a copy of the team with the given TeamNum from
// the most recent tick, or nil if no UpdateState has arrived or the
// team isn't present.
func (t *Store) TeamByNum(num int) *types.TeamRef {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.latest == nil {
		return nil
	}
	for i := range t.latest.Teams {
		if t.latest.Teams[i].TeamNum == num {
			team := t.latest.Teams[i]
			return &team
		}
	}
	return nil
}

// PlayerScalars returns Speed and bSupersonic for the player with the
// given canonical id from the latest tick. The first return is nil
// when the player is missing or RL didn't ship Speed (non-spectator
// builds omit it).
func (t *Store) PlayerScalars(id string) (*float64, bool) {
	if id == "" {
		return nil, false
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.latest == nil {
		return nil, false
	}
	for i := range t.latest.Players {
		p := &t.latest.Players[i]
		if p.ID != id {
			continue
		}
		if p.Speed == nil {
			return nil, false
		}
		sp := *p.Speed
		return &sp, p.Supersonic
	}
	return nil, false
}

// InReplay reports whether the most recent UpdateState had bReplay
// set. Used by emitters that gate on the replay edge but aren't the
// processor that published _GoalReplayStarted.
func (t *Store) InReplay() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.latest != nil && t.latest.BReplay
}
