package emit

import (
	"encoding/json"
	"rl-toolkit/backend/internal/bus"
	"rl-toolkit/backend/internal/types"
)

// TickDiff owns every UpdateState-driven diff event:
//
//   - _PlayerJoined / _PlayerLeft on roster identity changes
//     (any phase).
//   - _PlayerScoreChanged on per-player stat deltas (live phases).
//   - _BoostPickup on a rising boost edge (live phases).
//   - _TeamScoreChanged on team score deltas (live phases).
//   - _BallPossessionChanged on ball.TeamNum changes (live phases).
//
// The previous/current snapshots come from TickHistory (already
// observed before any emit processor runs); PhaseGate gates
// gameplay-only events; the shared Correlator feeds the triggeredBy
// lookup on _BallPossessionChanged. TickDiff also calls
// flipReset.ClearFlipResetArm(id) when a player transitions to
// bOnGround=true so Statfeed's flip-reset bookkeeping stays
// consistent.
type TickDiff struct {
	phase       PhaseGate
	ticks       TickHistory
	correlation Correlator
	flipReset   FlipResetClearer
}

func NewTickDiff(phase PhaseGate, ticks TickHistory, correlation Correlator, flipReset FlipResetClearer) *TickDiff {
	return &TickDiff{
		phase:       phase,
		ticks:       ticks,
		correlation: correlation,
		flipReset:   flipReset,
	}
}

func (e *TickDiff) Process(evt bus.Event) []bus.Event {
	if evt.Name != "UpdateState" {
		return nil
	}
	curr := e.ticks.Latest()
	prev := e.ticks.Previous()
	if curr == nil || prev == nil {
		return nil
	}
	// Different-match guard: when the match guid changes, every
	// "diff" against the previous snapshot would be misleading. The
	// new tick is a fresh baseline.
	if prev.MatchGUID != "" && curr.MatchGUID != "" && prev.MatchGUID != curr.MatchGUID {
		return nil
	}

	out := make([]bus.Event, 0)

	// Roster + per-player score diffs run in any phase. Players can
	// join/leave or have stats reconciled in lobby, podium, etc.
	out = append(out, e.diffPlayers(prev, curr)...)

	// Play-state diffs only make sense during active gameplay. RL
	// keeps streaming UpdateStates with stale flags during replays
	// and on the post-match screen; without the gate we'd publish
	// phantom _BoostPickup / _BallPossessionChanged /
	// _TeamScoreChanged events.
	if !liveGameplayPhases(e.phase) {
		return out
	}

	out = append(out, e.diffPlayersLive(prev, curr)...)
	out = append(out, e.diffTeamScores(prev, curr)...)
	if v := e.diffBallPossession(prev, curr); v != nil {
		out = append(out, *v)
	}
	return out
}

func (e *TickDiff) diffPlayers(prev, curr *types.TickSnapshot) []bus.Event {
	prevByID := make(map[string]*types.TickPlayer, len(prev.Players))
	for i := range prev.Players {
		p := &prev.Players[i]
		if p.ID != "" {
			prevByID[p.ID] = p
		}
	}
	currByID := make(map[string]*types.TickPlayer, len(curr.Players))
	for i := range curr.Players {
		p := &curr.Players[i]
		if p.ID != "" {
			currByID[p.ID] = p
		}
	}

	var out []bus.Event
	for id, p := range currByID {
		if _, was := prevByID[id]; was {
			continue
		}
		if v := e.playerEnvelope("_PlayerJoined", curr.MatchGUID, p); v != nil {
			out = append(out, *v)
		}
	}
	for id, p := range prevByID {
		if _, still := currByID[id]; still {
			continue
		}
		if v := e.playerEnvelope("_PlayerLeft", prev.MatchGUID, p); v != nil {
			out = append(out, *v)
		}
	}
	return out
}

func (e *TickDiff) playerEnvelope(eventName, guid string, p *types.TickPlayer) *bus.Event {
	if p == nil || p.ID == "" {
		return nil
	}
	enriched := &types.EnrichedPlayer{
		ID:       p.ID,
		Name:     p.Name,
		Team:     p.Team,
		Platform: types.PlatformFromID(p.ID),
		IsBot:    types.IsBotID(p.ID),
	}
	body, err := json.Marshal(struct {
		MatchGUID string                `json:"matchGuid,omitempty"`
		Player    *types.EnrichedPlayer `json:"player"`
		Phase     string                `json:"phase,omitempty"`
	}{
		MatchGUID: guid,
		Player:    enriched,
		Phase:     e.currentPhaseString(),
	})
	if err != nil {
		return nil
	}
	return &bus.Event{Name: eventName, Data: body}
}

func (e *TickDiff) currentPhaseString() string {
	if e.phase == nil {
		return ""
	}
	return string(e.phase.CurrentPhase())
}

func (e *TickDiff) diffPlayersLive(prev, curr *types.TickSnapshot) []bus.Event {
	prevByID := make(map[string]*types.TickPlayer, len(prev.Players))
	for i := range prev.Players {
		p := &prev.Players[i]
		if p.ID != "" {
			prevByID[p.ID] = p
		}
	}
	var out []bus.Event
	for i := range curr.Players {
		c := &curr.Players[i]
		if c.ID == "" {
			continue
		}
		p, ok := prevByID[c.ID]
		if !ok {
			continue
		}
		if v := e.playerScoreChanged(curr.MatchGUID, p, c); v != nil {
			out = append(out, *v)
		}
		if v := e.boostPickup(curr.MatchGUID, p, c); v != nil {
			out = append(out, *v)
		}
		if !p.OnGround && c.OnGround && e.flipReset != nil {
			e.flipReset.ClearFlipResetArm(c.ID)
		}
	}
	return out
}

// playerScoreChanged compares the seven non-spectator stat fields.
// Only sends a delta map for fields that actually moved.
func (e *TickDiff) playerScoreChanged(guid string, prev, curr *types.TickPlayer) *bus.Event {
	delta := map[string]int{}
	if curr.Score != prev.Score {
		delta["score"] = curr.Score - prev.Score
	}
	if curr.Goals != prev.Goals {
		delta["goals"] = curr.Goals - prev.Goals
	}
	if curr.Assists != prev.Assists {
		delta["assists"] = curr.Assists - prev.Assists
	}
	if curr.Saves != prev.Saves {
		delta["saves"] = curr.Saves - prev.Saves
	}
	if curr.Shots != prev.Shots {
		delta["shots"] = curr.Shots - prev.Shots
	}
	if curr.Touches != prev.Touches {
		delta["touches"] = curr.Touches - prev.Touches
	}
	if curr.Demos != prev.Demos {
		delta["demos"] = curr.Demos - prev.Demos
	}
	if len(delta) == 0 {
		return nil
	}
	enriched := &types.EnrichedPlayer{
		ID:       curr.ID,
		Name:     curr.Name,
		Team:     curr.Team,
		Platform: types.PlatformFromID(curr.ID),
		IsBot:    types.IsBotID(curr.ID),
	}
	body, err := json.Marshal(struct {
		MatchGUID string                `json:"matchGuid,omitempty"`
		Player    *types.EnrichedPlayer `json:"player"`
		Delta     map[string]int        `json:"delta"`
	}{
		MatchGUID: guid,
		Player:    enriched,
		Delta:     delta,
	})
	if err != nil {
		return nil
	}
	return &bus.Event{Name: "_PlayerScoreChanged", Data: body}
}

// boostPickup fires when the player's Boost increased (i.e., they
// picked up a pad or ran over a big-boost icon). Suppresses the
// post-respawn case (demolished → not demolished) — that's a boost
// reset, not a pickup. Also suppresses the first observation (no
// baseline), which happens when prev.Boost is nil (non-spectator
// blackout).
func (e *TickDiff) boostPickup(guid string, prev, curr *types.TickPlayer) *bus.Event {
	if curr.Boost == nil {
		return nil
	}
	// RL omits Boost when it's 0; treat nil as 0 so pickups from empty
	// boost are still detected.
	prevBoost := 0
	if prev.Boost != nil {
		prevBoost = *prev.Boost
	}
	if *curr.Boost <= prevBoost {
		return nil
	}
	if prev.Demolished && !curr.Demolished {
		// Respawn boost-reset, not a pickup.
		return nil
	}
	enriched := &types.EnrichedPlayer{
		ID:       curr.ID,
		Name:     curr.Name,
		Team:     curr.Team,
		Platform: types.PlatformFromID(curr.ID),
		IsBot:    types.IsBotID(curr.ID),
	}
	body, err := json.Marshal(struct {
		MatchGUID   string                `json:"matchGuid,omitempty"`
		Player      *types.EnrichedPlayer `json:"player"`
		BoostBefore int                   `json:"boostBefore"`
		BoostAfter  int                   `json:"boostAfter"`
		Delta       int                   `json:"delta"`
	}{
		MatchGUID:   guid,
		Player:      enriched,
		BoostBefore: prevBoost,
		BoostAfter:  *curr.Boost,
		Delta:       *curr.Boost - prevBoost,
	})
	if err != nil {
		return nil
	}
	return &bus.Event{Name: "_BoostPickup", Data: body}
}

// diffTeamScores fires _TeamScoreChanged when any team's Score moves.
// Distinct from _OwnGoal: this fires for every score delta, including
// regular goals.
func (e *TickDiff) diffTeamScores(prev, curr *types.TickSnapshot) []bus.Event {
	prevByNum := make(map[int]int, len(prev.Teams))
	for _, t := range prev.Teams {
		prevByNum[t.TeamNum] = t.Score
	}
	var out []bus.Event
	for _, t := range curr.Teams {
		old, ok := prevByNum[t.TeamNum]
		if !ok || t.Score == old {
			continue
		}
		body, err := json.Marshal(struct {
			MatchGUID string `json:"matchGuid,omitempty"`
			TeamNum   int    `json:"teamNum"`
			TeamName  string `json:"teamName,omitempty"`
			Before    int    `json:"before"`
			After     int    `json:"after"`
			Delta     int    `json:"delta"`
		}{
			MatchGUID: curr.MatchGUID,
			TeamNum:   t.TeamNum,
			TeamName:  t.Name,
			Before:    old,
			After:     t.Score,
			Delta:     t.Score - old,
		})
		if err == nil {
			out = append(out, bus.Event{Name: "_TeamScoreChanged", Data: body})
		}
	}
	return out
}

// diffBallPossession fires _BallPossessionChanged when the ball's
// TeamNum field changes. Normalizes 255 (RL's "untouched" sentinel)
// to null in the JSON via *int.
func (e *TickDiff) diffBallPossession(prev, curr *types.TickSnapshot) *bus.Event {
	if !prev.HasBall || !curr.HasBall || prev.BallTeam == curr.BallTeam {
		return nil
	}
	toNullable := func(team int) *int {
		if team == 255 {
			return nil
		}
		t := team
		return &t
	}
	body, err := json.Marshal(struct {
		MatchGUID   string                         `json:"matchGuid,omitempty"`
		Before      *int                           `json:"before"`
		After       *int                           `json:"after"`
		TriggeredBy *types.EnrichedCorrelatedTouch `json:"triggeredBy,omitempty"`
	}{
		MatchGUID:   curr.MatchGUID,
		Before:      toNullable(prev.BallTeam),
		After:       toNullable(curr.BallTeam),
		TriggeredBy: recentTouch(e.correlation, 3),
	})
	if err != nil {
		return nil
	}
	return &bus.Event{Name: "_BallPossessionChanged", Data: body}
}
