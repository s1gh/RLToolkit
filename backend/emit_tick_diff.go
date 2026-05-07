package main

import "encoding/json"

// flipResetClearer is the slim interface TickDiffEmitter calls when a
// player's bOnGround flag rises (clears any flip-reset arm so the
// next goal from that player isn't tagged unless they earn another
// reset run).
type flipResetClearer interface {
	ClearFlipResetArm(playerID string)
}

// TickDiffEmitter owns every UpdateState-driven diff event:
//
//   - _PlayerJoined / _PlayerLeft on roster identity changes
//     (any phase).
//   - _PlayerScoreChanged on per-player stat deltas (live phases).
//   - _BoostPickup on a rising boost edge (live phases).
//   - _TeamScoreChanged on team score deltas (live phases).
//   - _BallPossessionChanged on ball.TeamNum changes (live phases).
//
// The previous/current snapshots come from TickStore (already
// observed before any emit processor runs); MatchState gates
// gameplay-only events; the shared CorrelationBuffer feeds the
// triggeredBy lookup on _BallPossessionChanged. TickDiffEmitter
// also calls flipReset.ClearFlipResetArm(id) when a player
// transitions to bOnGround=true so StatfeedEmitter's flip-reset
// bookkeeping stays consistent.
type TickDiffEmitter struct {
	matchState  *MatchState
	ticks       *TickStore
	correlation *CorrelationBuffer
	flipReset   flipResetClearer
}

func NewTickDiffEmitter(
	ms *MatchState,
	ticks *TickStore,
	correlation *CorrelationBuffer,
	flipReset flipResetClearer,
) *TickDiffEmitter {
	return &TickDiffEmitter{
		matchState:  ms,
		ticks:       ticks,
		correlation: correlation,
		flipReset:   flipReset,
	}
}

func (e *TickDiffEmitter) Process(evt Event) []Event {
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
	if prev.matchGUID != "" && curr.matchGUID != "" && prev.matchGUID != curr.matchGUID {
		return nil
	}

	out := make([]Event, 0)

	// Roster + per-player score diffs run in any phase. Players can
	// join/leave or have stats reconciled in lobby, podium, etc.
	out = append(out, e.diffPlayers(prev, curr)...)

	// Play-state diffs only make sense during active gameplay. RL
	// keeps streaming UpdateStates with stale flags during replays
	// and on the post-match screen; without the gate we'd publish
	// phantom _BoostPickup / _BallPossessionChanged /
	// _TeamScoreChanged events.
	if e.matchState != nil {
		ph := e.matchState.Snapshot().Phase
		if ph != PhaseLive && ph != PhaseCountdown && ph != PhasePaused {
			return out
		}
	}

	out = append(out, e.diffPlayersLive(prev, curr)...)
	out = append(out, e.diffTeamScores(prev, curr)...)
	if v := e.diffBallPossession(prev, curr); v != nil {
		out = append(out, *v)
	}
	return out
}

func (e *TickDiffEmitter) diffPlayers(prev, curr *tickSnapshot) []Event {
	prevByID := make(map[string]*tickPlayer, len(prev.players))
	for i := range prev.players {
		p := &prev.players[i]
		if p.id != "" {
			prevByID[p.id] = p
		}
	}
	currByID := make(map[string]*tickPlayer, len(curr.players))
	for i := range curr.players {
		p := &curr.players[i]
		if p.id != "" {
			currByID[p.id] = p
		}
	}

	var out []Event
	for id, p := range currByID {
		if _, was := prevByID[id]; was {
			continue
		}
		if v := e.playerEnvelope("_PlayerJoined", curr.matchGUID, p); v != nil {
			out = append(out, *v)
		}
	}
	for id, p := range prevByID {
		if _, still := currByID[id]; still {
			continue
		}
		if v := e.playerEnvelope("_PlayerLeft", prev.matchGUID, p); v != nil {
			out = append(out, *v)
		}
	}
	return out
}

func (e *TickDiffEmitter) playerEnvelope(eventName, guid string, p *tickPlayer) *Event {
	if p == nil || p.id == "" {
		return nil
	}
	enriched := &EnrichedPlayer{
		ID:       p.id,
		Name:     p.name,
		Team:     p.team,
		Platform: platformFromID(p.id),
		IsBot:    isBotId(p.id),
	}
	body, err := json.Marshal(struct {
		MatchGUID string          `json:"matchGuid,omitempty"`
		Player    *EnrichedPlayer `json:"player"`
		Phase     string          `json:"phase,omitempty"`
	}{
		MatchGUID: guid,
		Player:    enriched,
		Phase:     e.currentPhaseString(),
	})
	if err != nil {
		return nil
	}
	return &Event{Name: eventName, Data: body}
}

func (e *TickDiffEmitter) currentPhaseString() string {
	if e.matchState == nil {
		return ""
	}
	return string(e.matchState.Snapshot().Phase)
}

func (e *TickDiffEmitter) diffPlayersLive(prev, curr *tickSnapshot) []Event {
	prevByID := make(map[string]*tickPlayer, len(prev.players))
	for i := range prev.players {
		p := &prev.players[i]
		if p.id != "" {
			prevByID[p.id] = p
		}
	}
	var out []Event
	for i := range curr.players {
		c := &curr.players[i]
		if c.id == "" {
			continue
		}
		p, ok := prevByID[c.id]
		if !ok {
			continue
		}
		if v := e.playerScoreChanged(curr.matchGUID, p, c); v != nil {
			out = append(out, *v)
		}
		if v := e.boostPickup(curr.matchGUID, p, c); v != nil {
			out = append(out, *v)
		}
		if !p.onGround && c.onGround && e.flipReset != nil {
			e.flipReset.ClearFlipResetArm(c.id)
		}
	}
	return out
}

// playerScoreChanged compares the seven non-spectator stat fields.
// Only sends a delta map for fields that actually moved.
func (e *TickDiffEmitter) playerScoreChanged(guid string, prev, curr *tickPlayer) *Event {
	delta := map[string]int{}
	if curr.score != prev.score {
		delta["score"] = curr.score - prev.score
	}
	if curr.goals != prev.goals {
		delta["goals"] = curr.goals - prev.goals
	}
	if curr.assists != prev.assists {
		delta["assists"] = curr.assists - prev.assists
	}
	if curr.saves != prev.saves {
		delta["saves"] = curr.saves - prev.saves
	}
	if curr.shots != prev.shots {
		delta["shots"] = curr.shots - prev.shots
	}
	if curr.touches != prev.touches {
		delta["touches"] = curr.touches - prev.touches
	}
	if curr.demos != prev.demos {
		delta["demos"] = curr.demos - prev.demos
	}
	if len(delta) == 0 {
		return nil
	}
	enriched := &EnrichedPlayer{
		ID:       curr.id,
		Name:     curr.name,
		Team:     curr.team,
		Platform: platformFromID(curr.id),
		IsBot:    isBotId(curr.id),
	}
	body, err := json.Marshal(struct {
		MatchGUID string          `json:"matchGuid,omitempty"`
		Player    *EnrichedPlayer `json:"player"`
		Delta     map[string]int  `json:"delta"`
	}{
		MatchGUID: guid,
		Player:    enriched,
		Delta:     delta,
	})
	if err != nil {
		return nil
	}
	return &Event{Name: "_PlayerScoreChanged", Data: body}
}

// boostPickup fires when the player's Boost increased (i.e., they
// picked up a pad or ran over a big-boost icon). Suppresses the
// post-respawn case (demolished → not demolished) — that's a boost
// reset, not a pickup. Also suppresses the first observation (no
// baseline), which happens when prev.boost is nil (non-spectator
// blackout).
func (e *TickDiffEmitter) boostPickup(guid string, prev, curr *tickPlayer) *Event {
	if curr.boost == nil {
		return nil
	}
	// RL omits Boost when it's 0; treat nil as 0 so pickups from empty
	// boost are still detected.
	prevBoost := 0
	if prev.boost != nil {
		prevBoost = *prev.boost
	}
	if *curr.boost <= prevBoost {
		return nil
	}
	if prev.demolished && !curr.demolished {
		// Respawn boost-reset, not a pickup.
		return nil
	}
	enriched := &EnrichedPlayer{
		ID:       curr.id,
		Name:     curr.name,
		Team:     curr.team,
		Platform: platformFromID(curr.id),
		IsBot:    isBotId(curr.id),
	}
	body, err := json.Marshal(struct {
		MatchGUID   string          `json:"matchGuid,omitempty"`
		Player      *EnrichedPlayer `json:"player"`
		BoostBefore int             `json:"boostBefore"`
		BoostAfter  int             `json:"boostAfter"`
		Delta       int             `json:"delta"`
	}{
		MatchGUID:   guid,
		Player:      enriched,
		BoostBefore: prevBoost,
		BoostAfter:  *curr.boost,
		Delta:       *curr.boost - prevBoost,
	})
	if err != nil {
		return nil
	}
	return &Event{Name: "_BoostPickup", Data: body}
}

// diffTeamScores fires _TeamScoreChanged when any team's Score moves.
// Distinct from _OwnGoal: this fires for every score delta, including
// regular goals.
func (e *TickDiffEmitter) diffTeamScores(prev, curr *tickSnapshot) []Event {
	prevByNum := make(map[int]int, len(prev.teams))
	for _, t := range prev.teams {
		prevByNum[t.TeamNum] = t.Score
	}
	var out []Event
	for _, t := range curr.teams {
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
			MatchGUID: curr.matchGUID,
			TeamNum:   t.TeamNum,
			TeamName:  t.Name,
			Before:    old,
			After:     t.Score,
			Delta:     t.Score - old,
		})
		if err == nil {
			out = append(out, Event{Name: "_TeamScoreChanged", Data: body})
		}
	}
	return out
}

// diffBallPossession fires _BallPossessionChanged when the ball's
// TeamNum field changes. Normalizes 255 (RL's "untouched" sentinel)
// to null in the JSON via *int.
func (e *TickDiffEmitter) diffBallPossession(prev, curr *tickSnapshot) *Event {
	if !prev.hasBall || !curr.hasBall || prev.ballTeam == curr.ballTeam {
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
		MatchGUID   string                   `json:"matchGuid,omitempty"`
		Before      *int                     `json:"before"`
		After       *int                     `json:"after"`
		TriggeredBy *enrichedCorrelatedTouch `json:"triggeredBy,omitempty"`
	}{
		MatchGUID:   curr.matchGUID,
		Before:      toNullable(prev.ballTeam),
		After:       toNullable(curr.ballTeam),
		TriggeredBy: recentTouch(e.correlation, 3),
	})
	if err != nil {
		return nil
	}
	return &Event{Name: "_BallPossessionChanged", Data: body}
}
