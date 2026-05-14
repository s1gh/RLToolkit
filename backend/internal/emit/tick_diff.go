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
//   - _BoostConsumed on a falling boost edge (live phases).
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
// PrimaryIdResolver enriches a TickPlayer-derived ID into a stamped
// EnrichedPlayer (carrying IsMe and roster fields). TickDiff uses it
// so boost-edge events ship the same enrichment shape the rest of
// the pipeline produces — without it, plugins filtering by isMe
// silently drop every event because IsMe stays false.
type PrimaryIdResolver interface {
	ResolveByPrimaryId(id string) *types.EnrichedPlayer
}

type TickDiff struct {
	phase       PhaseGate
	ticks       TickHistory
	correlation Correlator
	flipReset   FlipResetClearer
	roster      PrimaryIdResolver

	// pendingRespawnSuppress[playerID] = true means the player's
	// Demolished edge true→false was just observed; the next boost
	// decrease for that player is the post-respawn reseed (RL drops
	// boost to 33 a tick or two after the respawn lands) and should
	// be swallowed once. Cleared on match end so a stale flag from
	// the previous match can't swallow the first real spend of the
	// next.
	pendingRespawnSuppress map[string]bool

	// pickupStreaks[playerID] tracks an in-progress boost pickup. RL
	// ramps the Boost field over 2-3 ticks for a single physical pad
	// pickup, so a naive "one event per rising tick" emits multiple
	// _BoostPickup events for one pad. We accumulate consecutive
	// rising ticks into a streak and emit a single _BoostPickup when
	// the rise stops (boost plateaus or starts falling). delta on the
	// emitted event is the cumulative gain across the streak.
	pickupStreaks map[string]*pickupStreak
}

// pickupStreak holds the in-progress accumulation for one player.
// startBoost is the Boost value on the tick before the rise began;
// lastBoost is the latest observed value while still rising.
type pickupStreak struct {
	startBoost int
	lastBoost  int
}

func NewTickDiff(phase PhaseGate, ticks TickHistory, correlation Correlator, flipReset FlipResetClearer, roster PrimaryIdResolver) *TickDiff {
	return &TickDiff{
		phase:                  phase,
		ticks:                  ticks,
		correlation:            correlation,
		flipReset:              flipReset,
		roster:                 roster,
		pendingRespawnSuppress: make(map[string]bool),
		pickupStreaks:          make(map[string]*pickupStreak),
	}
}

// armKickoffSuppression flags every player currently above 33 boost
// for one-shot _BoostConsumed suppression. RL resets every player's
// boost to 33 between CountdownBegin and RoundStarted; without this
// guard, the resulting tick-over-tick decrease (e.g. 80→33) gets
// emitted as a 47-point spend and inflates session totals. Players at
// 33 or below skip the flag — they won't see a decrease at all.
// Pickup streaks are also dropped: anything mid-pickup at the goal
// moment is no longer trustworthy across the reset.
func (e *TickDiff) armKickoffSuppression() {
	if e.ticks == nil {
		return
	}
	curr := e.ticks.Latest()
	if curr == nil {
		return
	}
	if e.pendingRespawnSuppress == nil {
		e.pendingRespawnSuppress = make(map[string]bool)
	}
	for i := range curr.Players {
		p := &curr.Players[i]
		if p.ID == "" || p.Boost == nil {
			continue
		}
		if *p.Boost > 33 {
			e.pendingRespawnSuppress[p.ID] = true
		}
		if e.pickupStreaks != nil {
			delete(e.pickupStreaks, p.ID)
		}
	}
}

// matchEnded clears the per-match respawn-suppression bookkeeping and
// any in-progress pickup streaks. Called from Process on
// MatchCreated/MatchDestroyed so a flag armed in one match can't carry
// over and silently swallow the first real boost spend in the next,
// and so a half-accumulated pickup from the previous match doesn't
// flush with stale numbers on the first tick of the new one.
func (e *TickDiff) matchEnded(_ string) {
	for k := range e.pendingRespawnSuppress {
		delete(e.pendingRespawnSuppress, k)
	}
	for k := range e.pickupStreaks {
		delete(e.pickupStreaks, k)
	}
}

func (e *TickDiff) Process(evt bus.Event) []bus.Event {
	if evt.Name == "MatchCreated" || evt.Name == "MatchDestroyed" {
		e.matchEnded(evt.Name)
		return nil
	}
	if evt.Name == "CountdownBegin" {
		e.armKickoffSuppression()
		return nil
	}
	if evt.Name == "RoundStarted" {
		// By the time live play resumes, any kickoff boost reset has
		// already landed. Clear any flags still armed so they can't
		// silently swallow a real boost spend later.
		for k := range e.pendingRespawnSuppress {
			delete(e.pendingRespawnSuppress, k)
		}
		return nil
	}
	if evt.Name != "UpdateState" {
		return nil
	}
	curr := e.ticks.Latest()
	prev := e.ticks.Previous()
	if curr == nil || prev == nil {
		return nil
	}
	// A match guid change means the new tick is a fresh baseline; any
	// diff against the previous snapshot would be misleading. Includes
	// transitions to/from an empty guid (e.g. real match -> freeplay,
	// where one side of the boundary tick can ship an empty MatchGUID
	// before the freeplay session settles). Without this, the
	// post-match scoreboard (Goals=N, Saves=N) gets diffed against the
	// freeplay reset (Goals=0, Saves=0) and the resulting negative
	// delta is applied by stat plugins as a decrement.
	if prev.MatchGUID != curr.MatchGUID {
		e.matchEnded(prev.MatchGUID)
		return nil
	}

	out := make([]bus.Event, 0)

	// Roster + per-player score diffs run in any phase (join/leave
	// and stat reconciliation can happen in lobby, podium, etc.).
	out = append(out, e.diffPlayers(prev, curr)...)

	// Play-state diffs only make sense during active gameplay; RL
	// keeps streaming stale flags during replays and post-match.
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
		if v := e.boostConsumed(curr.MatchGUID, p, c); v != nil {
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
	// Route through the roster resolver so IsMe (and any other
	// roster-derived fields) get stamped. A manual EnrichedPlayer
	// build here would leave IsMe as false even for the local
	// player, which silently breaks any plugin filtering on isMe.
	var enriched *types.EnrichedPlayer
	if e.roster != nil {
		enriched = e.roster.ResolveByPrimaryId(curr.ID)
	}
	if enriched == nil {
		enriched = &types.EnrichedPlayer{
			ID:       curr.ID,
			Name:     curr.Name,
			Team:     curr.Team,
			Platform: types.PlatformFromID(curr.ID),
			IsBot:    types.IsBotID(curr.ID),
		}
	} else if enriched.Name == "" {
		// ResolveByPrimaryId returns a minimal stub if the player
		// isn't in the current roster snapshot yet (early ticks).
		// Fill the name/team from the live tick so the payload is
		// still useful.
		enriched.Name = curr.Name
		enriched.Team = curr.Team
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

// boostPickup emits one _BoostPickup per physical pad pickup. RL
// ramps the Boost field over 2-3 ticks for a single pad, so a "one
// event per rising tick" approach over-counts. We accumulate
// consecutive rising ticks into a streak and emit the event when the
// rise stops (boost plateaus or falls).
//
//   - Rising tick (curr > prev): open or extend a streak for the
//     player; return nil (no event yet).
//   - Non-rising tick (curr <= prev): if a streak was open, flush it
//     as one _BoostPickup; otherwise nothing to do.
//
// Suppressions:
//   - Respawn edge (Demolished true→false): the boost jump from 0 to
//     33 is RL reseeding the spawn boost, not a pickup. Drop any open
//     streak and don't open a new one for this tick's rise.
//   - curr.Boost nil: non-spectator mode dropped the field; drop any
//     open streak and emit nothing.
func (e *TickDiff) boostPickup(guid string, prev, curr *types.TickPlayer) *bus.Event {
	if curr.Boost == nil {
		e.dropPickupStreak(curr.ID)
		return nil
	}
	// RL omits Boost when 0; treat nil as 0 so pickups from empty
	// boost still register.
	prevBoost := 0
	if prev.Boost != nil {
		prevBoost = *prev.Boost
	}
	respawnEdge := prev.Demolished && !curr.Demolished
	rising := *curr.Boost > prevBoost

	if respawnEdge {
		// The post-respawn reseed isn't a pickup. Discard any
		// in-flight streak so the reseed tick can't extend it.
		e.dropPickupStreak(curr.ID)
		return nil
	}

	if rising {
		streak, ok := e.pickupStreaks[curr.ID]
		if !ok {
			if e.pickupStreaks == nil {
				e.pickupStreaks = make(map[string]*pickupStreak)
			}
			e.pickupStreaks[curr.ID] = &pickupStreak{
				startBoost: prevBoost,
				lastBoost:  *curr.Boost,
			}
		} else {
			streak.lastBoost = *curr.Boost
		}
		return nil
	}

	// Not rising: flush any open streak as a single event.
	streak, ok := e.pickupStreaks[curr.ID]
	if !ok {
		return nil
	}
	delete(e.pickupStreaks, curr.ID)
	return e.makePickupEvent(guid, curr, streak.startBoost, streak.lastBoost)
}

// dropPickupStreak removes any in-flight streak for the player without
// emitting. Used when the data is no longer trustworthy (respawn
// reseed, missing Boost field).
func (e *TickDiff) dropPickupStreak(id string) {
	if id == "" {
		return
	}
	if _, ok := e.pickupStreaks[id]; ok {
		delete(e.pickupStreaks, id)
	}
}

// makePickupEvent builds the _BoostPickup envelope for a flushed
// streak. Mirrors the resolver+enrichment shape used by
// boostConsumed so the two events expose identical player payloads.
func (e *TickDiff) makePickupEvent(guid string, curr *types.TickPlayer, beforeBoost, afterBoost int) *bus.Event {
	var enriched *types.EnrichedPlayer
	if e.roster != nil {
		enriched = e.roster.ResolveByPrimaryId(curr.ID)
	}
	if enriched == nil {
		enriched = &types.EnrichedPlayer{
			ID:       curr.ID,
			Name:     curr.Name,
			Team:     curr.Team,
			Platform: types.PlatformFromID(curr.ID),
			IsBot:    types.IsBotID(curr.ID),
		}
	} else if enriched.Name == "" {
		enriched.Name = curr.Name
		enriched.Team = curr.Team
	}
	// Small pads max out at 12 boost; anything with a cumulative rise
	// of 20+ is unambiguously a big pad. Plugins should filter on this
	// flag instead of boostAfter==100, since RL clamps mid-rise: a big
	// pad picked up while consuming boost can leave lastBoost at 88,
	// 92, 95, etc.
	delta := afterBoost - beforeBoost
	body, err := json.Marshal(struct {
		MatchGUID   string                `json:"matchGuid,omitempty"`
		Player      *types.EnrichedPlayer `json:"player"`
		BoostBefore int                   `json:"boostBefore"`
		BoostAfter  int                   `json:"boostAfter"`
		Delta       int                   `json:"delta"`
		IsBigPad    bool                  `json:"isBigPad"`
	}{
		MatchGUID:   guid,
		Player:      enriched,
		BoostBefore: beforeBoost,
		BoostAfter:  afterBoost,
		Delta:       delta,
		IsBigPad:    delta >= 20,
	})
	if err != nil {
		return nil
	}
	return &bus.Event{Name: "_BoostPickup", Data: body}
}

// boostConsumed mirrors boostPickup on the falling edge: every time
// Boost decreased between ticks (active boost spend), emit
// _BoostConsumed with the spent amount. Suppresses the respawn boost
// reset (was demolished, now isn't) so a death doesn't get counted as
// a 100→33 spend, mirroring the demolished-edge guard in boostPickup.
// Also no-ops when curr.Boost is nil (non-spectator mode dropping the
// field).
func (e *TickDiff) boostConsumed(guid string, prev, curr *types.TickPlayer) *bus.Event {
	// Arm respawn suppression on the Demolished true→false edge for
	// this player, regardless of whether boost moved. The actual
	// reseed-to-33 lands a tick or two later, so we have to remember
	// the respawn until we see the boost drop. Lazy-init the map in
	// case TickDiff was constructed without going through New (tests).
	if prev.Demolished && !curr.Demolished && curr.ID != "" {
		if e.pendingRespawnSuppress == nil {
			e.pendingRespawnSuppress = make(map[string]bool)
		}
		e.pendingRespawnSuppress[curr.ID] = true
	}
	if curr.Boost == nil {
		return nil
	}
	prevBoost := 0
	if prev.Boost != nil {
		prevBoost = *prev.Boost
	}
	if *curr.Boost >= prevBoost {
		return nil
	}
	// One-shot suppression: if this player respawned within the last
	// few ticks (or on this tick), the next boost decrease is the
	// post-respawn reseed. Swallow it and clear the flag so
	// subsequent real spends fire normally.
	if e.pendingRespawnSuppress[curr.ID] {
		delete(e.pendingRespawnSuppress, curr.ID)
		return nil
	}
	// Route through the roster resolver so IsMe (and any other
	// roster-derived fields) get stamped. A manual EnrichedPlayer
	// build here would leave IsMe as false even for the local
	// player, which silently breaks any plugin filtering on isMe.
	var enriched *types.EnrichedPlayer
	if e.roster != nil {
		enriched = e.roster.ResolveByPrimaryId(curr.ID)
	}
	if enriched == nil {
		enriched = &types.EnrichedPlayer{
			ID:       curr.ID,
			Name:     curr.Name,
			Team:     curr.Team,
			Platform: types.PlatformFromID(curr.ID),
			IsBot:    types.IsBotID(curr.ID),
		}
	} else if enriched.Name == "" {
		// ResolveByPrimaryId returns a minimal stub if the player
		// isn't in the current roster snapshot yet (early ticks).
		// Fill the name/team from the live tick so the payload is
		// still useful.
		enriched.Name = curr.Name
		enriched.Team = curr.Team
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
		Delta:       prevBoost - *curr.Boost,
	})
	if err != nil {
		return nil
	}
	return &bus.Event{Name: "_BoostConsumed", Data: body}
}

// diffTeamScores fires _TeamScoreChanged on any team's Score move —
// every score delta, including regular goals (distinct from _OwnGoal).
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
