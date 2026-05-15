package emit

import (
	"encoding/json"
	"rl-toolkit/backend/internal/bus"
	"rl-toolkit/backend/internal/types"
	"rl-toolkit/backend/internal/wire"
	"sync"
)

// goalModifiers carries the same-frame statfeed flags RL fires
// alongside a goal. Every field is always present in the JSON output
// so consumers get explicit false values instead of missing keys.
// IsFlipResetGoal is toolkit-detected, not from a Statfeed: scorer
// got a FlipReset and stayed airborne until scoring.
type goalModifiers struct {
	IsAerialGoal     bool `json:"isAerialGoal"`
	IsBackwardsGoal  bool `json:"isBackwardsGoal"`
	IsBicycleGoal    bool `json:"isBicycleGoal"`
	IsLongGoal       bool `json:"isLongGoal"`
	IsTurtleGoal     bool `json:"isTurtleGoal"`
	IsOvertimeGoal   bool `json:"isOvertimeGoal"`
	IsPoolShot       bool `json:"isPoolShot"`
	IsHoopsSwishGoal bool `json:"isHoopsSwishGoal"`
	IsHatTrickGoal   bool `json:"isHatTrickGoal"`
	IsFlipResetGoal  bool `json:"isFlipResetGoal"`
	IsStolenGoal     bool `json:"isStolenGoal"`
}

// modifierStatfeedNames maps the statfeed EventName (as RL ships it)
// to the goalModifiers boolean field that should be set. Only
// same-player matches against the scorer count.
var modifierStatfeedNames = map[string]func(*goalModifiers){
	"AerialGoal":     func(m *goalModifiers) { m.IsAerialGoal = true },
	"BackwardsGoal":  func(m *goalModifiers) { m.IsBackwardsGoal = true },
	"BicycleGoal":    func(m *goalModifiers) { m.IsBicycleGoal = true },
	"LongGoal":       func(m *goalModifiers) { m.IsLongGoal = true },
	"TurtleGoal":     func(m *goalModifiers) { m.IsTurtleGoal = true },
	"OvertimeGoal":   func(m *goalModifiers) { m.IsOvertimeGoal = true },
	"PoolShot":       func(m *goalModifiers) { m.IsPoolShot = true },
	"HoopsSwishGoal": func(m *goalModifiers) { m.IsHoopsSwishGoal = true },
	"HatTrick":       func(m *goalModifiers) { m.IsHatTrickGoal = true },
}

// sameShortcutPlayer compares two shortcut refs for "same player".
// RL's Shortcut is the per-match slot index (number); Name is the
// human-readable form. Either match counts; both unset means no
// match.
func sameShortcutPlayer(a, b *types.ShortcutRef) bool {
	if a == nil || b == nil {
		return false
	}
	if a.Shortcut != 0 && b.Shortcut != 0 && a.Shortcut == b.Shortcut {
		return true
	}
	if a.Name != "" && b.Name != "" && a.Name == b.Name {
		return true
	}
	return false
}

type enrichedGoalScored struct {
	Event          string                       `json:"Event"`
	MatchGUID      string                       `json:"matchGuid,omitempty"`
	Scorer         *types.EnrichedPlayer        `json:"scorer,omitempty"`
	Assister       *types.EnrichedPlayer        `json:"assister,omitempty"`
	BallLastTouch  *types.EnrichedBallLastTouch `json:"ballLastTouch,omitempty"`
	GoalSpeed      *float64                     `json:"goalSpeed,omitempty"`
	GoalTime       *float64                     `json:"goalTime,omitempty"`
	ImpactLocation *types.Vec3                  `json:"impactLocation,omitempty"`
	ScoringTeam    *int                         `json:"scoringTeam,omitempty"`
	ConcedingTeam  *int                         `json:"concedingTeam,omitempty"`
	IsOwnGoal      bool                         `json:"isOwnGoal"`
	Modifiers      *goalModifiers               `json:"modifiers"`
}

// Goal publishes _GoalScored (with modifiers + last-touch + own-goal
// heuristic) and _GoalReplayStarted (on the bReplay rising edge during
// a goal celebration).
//
// Reads:
//   - roster — resolve scorer/assister/ballLastTouch refs.
//   - correlation — same-frame statfeed modifiers; fallback for
//     missing BallLastTouch via Recent("BallHit", 1).
//   - ticks — bReplay edge detection for _GoalReplayStarted.
//   - flipReset — consume the per-player arm for IsFlipResetGoal.
//   - goals — bump on non-own-goal, look up for HatTrick suppression.
//
// Writes:
//   - correlation: Records ("_GoalScored", GoalRecord) so OwnGoal /
//     Statfeed (Assist) can correlate.
type Goal struct {
	roster      RosterResolver
	correlation Correlator
	ticks       TickHistory
	flipReset   FlipResetConsumer
	goals       GoalCounter

	mu         sync.Mutex
	lastGoal   *enrichedGoalScored
	prevReplay bool
}

func NewGoal(
	roster RosterResolver,
	correlation Correlator,
	ticks TickHistory,
	flipReset FlipResetConsumer,
	goals GoalCounter,
) *Goal {
	return &Goal{
		roster:      roster,
		correlation: correlation,
		ticks:       ticks,
		flipReset:   flipReset,
		goals:       goals,
	}
}

func (e *Goal) Process(evt bus.Event) []bus.Event {
	switch evt.Name {
	case "MatchCreated", "MatchDestroyed":
		e.mu.Lock()
		e.lastGoal = nil
		e.prevReplay = false
		e.mu.Unlock()
		return nil
	case "GoalScored":
		return e.processGoal(evt)
	case "UpdateState":
		return e.maybeReplayStarted()
	}
	return nil
}

func (e *Goal) processGoal(evt bus.Event) []bus.Event {
	inner := wire.UnwrapInnerData(evt.Raw)
	if inner == "" {
		return nil
	}
	var d types.GoalScoredData
	if err := json.Unmarshal([]byte(inner), &d); err != nil {
		return nil
	}

	scorerRef := d.Scorer
	if scorerRef == nil {
		scorerRef = d.ScorerLow
	}
	// RL re-fires GoalScored at round-restart with an empty Scorer.Name;
	// dropping it here prevents a phantom "Unknown" scorer from
	// overwriting the previous goal in any plugin that caches it.
	if scorerRef == nil || scorerRef.Name == "" {
		return nil
	}
	scorer := e.roster.ResolveByShortcut(*scorerRef)
	if scorer == nil {
		return nil
	}

	guid := wire.PickStr(d.MatchGUID, d.MatchGUIDLow)
	assisterRef := d.Assister
	if assisterRef == nil {
		assisterRef = d.AssisterLow
	}
	lastTouch := d.BallLastTouch
	if lastTouch == nil {
		lastTouch = d.BallLastTouchLow
	}
	loc := d.ImpactLocation
	if loc == nil {
		loc = d.ImpactLocationLow
	}

	scoringTeam := scorer.Team
	concedingTeam := 1 - scoringTeam
	out := enrichedGoalScored{
		Event:          "_GoalScored",
		MatchGUID:      guid,
		Scorer:         scorer,
		GoalSpeed:      wire.PickFloat(d.GoalSpeed, d.GoalSpeedLow),
		GoalTime:       wire.PickFloat(d.GoalTime, d.GoalTimeLow),
		ImpactLocation: loc,
		ScoringTeam:    &scoringTeam,
		ConcedingTeam:  &concedingTeam,
	}

	if assisterRef != nil && assisterRef.Name != "" {
		out.Assister = e.roster.ResolveByShortcut(*assisterRef)
	}

	var lastToucher *types.EnrichedPlayer
	if lastTouch != nil {
		ref := lastTouch.Player
		if ref == nil {
			ref = lastTouch.PlayerLow
		}
		sp := wire.PickFloat(lastTouch.Speed, lastTouch.SpeedLow)
		if ref != nil {
			lastToucher = e.roster.ResolveByShortcut(*ref)
		}
		if lastToucher != nil || sp != nil {
			out.BallLastTouch = &types.EnrichedBallLastTouch{
				Player: lastToucher,
				Speed:  sp,
			}
		}
	}
	// Fallback: when RL ships GoalScored without a BallLastTouch
	// block (observed on some builds, especially for own goals), use
	// the most recent BallHit from the shared correlation buffer.
	if lastToucher == nil {
		for _, p := range e.correlation.Recent("BallHit", 1) {
			if rec, ok := p.(*types.BallHitRecord); ok && rec != nil {
				lastToucher = rec.Player
			}
		}
	}
	// Own-goal heuristic: scorer is on the opposing team and the
	// deflector (BallLastTouch) is on the conceding team. Solo own
	// goals can't be distinguished here — _OwnGoal handles them via
	// score-delta verification.
	if lastToucher != nil && lastToucher.Team == concedingTeam {
		out.IsOwnGoal = true
	}

	// Bump the per-player real-goal counter on honest goals.
	// RL's HatTrick counter includes own goals; we filter them out
	// to verify the threshold against actual scoring. The bumped
	// count is also the authoritative signal for _HatTrick emission
	// below.
	var realGoalsAfter int
	if !out.IsOwnGoal && e.goals != nil {
		e.goals.BumpRealGoals(scorer.ID)
		realGoalsAfter = e.goals.RealGoals(scorer.ID)
	}

	mods := collectGoalModifiers(e.correlation, scorerRef)

	// HatTrick suppression: clear the modifier when the player's
	// honest-goal count is below 3.
	if mods.IsHatTrickGoal && e.goals != nil && e.goals.RealGoals(scorer.ID) < 3 {
		mods.IsHatTrickGoal = false
	}

	// Flip-reset goal: scorer was airborne with a flip reset and
	// hadn't touched ground since. Consume the arm.
	if e.flipReset != nil && e.flipReset.ConsumeFlipResetArm(scorer.ID) {
		mods.IsFlipResetGoal = true
	}
	mods.IsStolenGoal = detectStolenGoal(scorer, lastToucher, out.IsOwnGoal, e.correlation)
	out.Modifiers = mods

	// Record the resolved goal so _OwnGoal can attach it as
	// `correlatedGoal` and _Assist can find its companion.
	e.correlation.Record("_GoalScored", &types.GoalRecord{
		Scorer:        scorer,
		ScoringTeam:   scoringTeam,
		ConcedingTeam: concedingTeam,
	})

	// Cache the full enriched envelope so the bReplay rising edge can
	// mirror it as _GoalReplayStarted. The correlation buffer gets
	// evicted by burst BallHit/StatfeedEvent traffic during the
	// celebration, hence the dedicated copy.
	cached := out
	e.mu.Lock()
	e.lastGoal = &cached
	e.mu.Unlock()

	body, err := marshalGoalBody(out)
	if err != nil {
		return nil
	}
	events := []bus.Event{{Name: "_GoalScored", Data: body}}

	// _HatTrick fires when the scorer's real-goal counter hits
	// exactly 3. We emit from here (not from Statfeed) because RL's
	// HatTrick statfeed is unreliable: it skips, delays, and sometimes
	// fires globally every 3 goals instead of per scorer.
	if realGoalsAfter == 3 {
		if hat := buildHatTrick(guid, scorer); hat != nil {
			events = append(events, *hat)
		}
	}
	return events
}

// buildHatTrick assembles the _HatTrick bus event for `scorer`.
// GoalsThisMatch is hardcoded to 3 because this fires only on the
// scorer's third real goal; subsequent goals don't re-trigger.
func buildHatTrick(guid string, scorer *types.EnrichedPlayer) *bus.Event {
	if scorer == nil {
		return nil
	}
	body, err := json.Marshal(struct {
		MatchGUID      string                `json:"matchGuid,omitempty"`
		MainTarget     *types.EnrichedPlayer `json:"mainTarget"`
		GoalsThisMatch int                   `json:"goalsThisMatch"`
	}{
		MatchGUID:      guid,
		MainTarget:     scorer,
		GoalsThisMatch: 3,
	})
	if err != nil {
		return nil
	}
	return &bus.Event{Name: "_HatTrick", Data: body}
}

func (e *Goal) maybeReplayStarted() []bus.Event {
	curr := e.ticks.Latest()
	if curr == nil {
		return nil
	}
	e.mu.Lock()
	prev := e.prevReplay
	e.prevReplay = curr.BReplay
	cached := e.lastGoal
	e.mu.Unlock()
	if prev || !curr.BReplay || cached == nil {
		return nil
	}
	out := *cached
	out.Event = "_GoalReplayStarted"
	out.MatchGUID = curr.MatchGUID
	body, err := marshalGoalBody(out)
	if err != nil {
		return nil
	}
	return []bus.Event{{Name: "_GoalReplayStarted", Data: body}}
}

// marshalGoalBody encodes an enrichedGoalScored as just its data
// payload (no top-level Event field — the bus envelope provides
// that).
func marshalGoalBody(g enrichedGoalScored) ([]byte, error) {
	return json.Marshal(struct {
		MatchGUID      string                       `json:"matchGuid,omitempty"`
		Scorer         *types.EnrichedPlayer        `json:"scorer,omitempty"`
		Assister       *types.EnrichedPlayer        `json:"assister,omitempty"`
		BallLastTouch  *types.EnrichedBallLastTouch `json:"ballLastTouch,omitempty"`
		GoalSpeed      *float64                     `json:"goalSpeed,omitempty"`
		GoalTime       *float64                     `json:"goalTime,omitempty"`
		ImpactLocation *types.Vec3                  `json:"impactLocation,omitempty"`
		ScoringTeam    *int                         `json:"scoringTeam,omitempty"`
		ConcedingTeam  *int                         `json:"concedingTeam,omitempty"`
		IsOwnGoal      bool                         `json:"isOwnGoal"`
		Modifiers      *goalModifiers               `json:"modifiers,omitempty"`
	}{
		MatchGUID:      g.MatchGUID,
		Scorer:         g.Scorer,
		Assister:       g.Assister,
		BallLastTouch:  g.BallLastTouch,
		GoalSpeed:      g.GoalSpeed,
		GoalTime:       g.GoalTime,
		ImpactLocation: g.ImpactLocation,
		ScoringTeam:    g.ScoringTeam,
		ConcedingTeam:  g.ConcedingTeam,
		IsOwnGoal:      g.IsOwnGoal,
		Modifiers:      g.Modifiers,
	})
}

// collectGoalModifiers walks the correlation buffer for same-player
// modifier statfeeds. Always returns a non-nil struct so consumers
// get explicit false values for every modifier field. Consumes the
// matched modifier statfeeds so they don't bleed into the next goal.
func collectGoalModifiers(c Correlator, scorer *types.ShortcutRef) *goalModifiers {
	mods := &goalModifiers{}
	if scorer == nil {
		return mods
	}
	apply := func(name string) {
		setter, ok := modifierStatfeedNames[name]
		if !ok {
			return
		}
		setter(mods)
	}
	for _, p := range c.Recent("StatfeedEvent", 15) {
		rec, ok := p.(*types.StatfeedRecord)
		if !ok || rec.MainRef == nil {
			continue
		}
		if !sameShortcutPlayer(rec.MainRef, scorer) {
			continue
		}
		apply(rec.EventName)
	}

	// Consume modifier statfeeds so they don't apply to the next goal.
	c.RemoveByName("StatfeedEvent", func(p interface{}) bool {
		rec, ok := p.(*types.StatfeedRecord)
		if !ok || rec.MainRef == nil {
			return false
		}
		_, isModifier := modifierStatfeedNames[rec.EventName]
		return isModifier && sameShortcutPlayer(rec.MainRef, scorer)
	})

	return mods
}

// detectStolenGoal returns true when the scorer finished a teammate's
// shot with no other BallHit in between. Event-sequence based, so it
// is independent of the user's configured PacketSendRate.
//
// Criteria:
//   - not an own goal,
//   - scorer is the resolved last toucher,
//   - the BallHit immediately before the scorer's last touch was by a
//     different player on the same team (a teammate),
//   - that teammate has a Shot statfeed in the recent buffer that was
//     recorded AFTER any opponent BallHit (so an old unrelated Shot
//     can't credit a later goal just because it's still in the buffer).
//
// Consumes the matched Shot statfeed so a teammate's earlier
// unconverted Shot cannot spuriously credit a later goal.
func detectStolenGoal(
	scorer *types.EnrichedPlayer,
	lastToucher *types.EnrichedPlayer,
	isOwnGoal bool,
	c Correlator,
) bool {
	if isOwnGoal || scorer == nil || lastToucher == nil {
		return false
	}
	if lastToucher.ID != scorer.ID {
		return false
	}
	hits := c.Recent("BallHit", 2)
	if len(hits) < 2 {
		return false
	}
	prev, ok := hits[1].(*types.BallHitRecord)
	if !ok || prev == nil || prev.Player == nil {
		return false
	}
	teammate := prev.Player
	if teammate.ID == scorer.ID {
		return false
	}
	if teammate.Team != scorer.Team {
		return false
	}
	teammateRef := &types.ShortcutRef{Name: teammate.Name}

	// Walk BallHits and Shot statfeeds together in chronological
	// order (newest first). The teammate's Shot must appear before
	// (i.e. more recently than) any opponent BallHit — otherwise the
	// statfeed is from an earlier play that opponents have already
	// touched the ball during, which means the goal isn't actually
	// finishing this teammate's shot.
	mixed := c.WalkRecent([]string{"BallHit", "StatfeedEvent"}, 30)
	for _, m := range mixed {
		switch m.Name {
		case "BallHit":
			rec, ok := m.Payload.(*types.BallHitRecord)
			if !ok || rec == nil || rec.Player == nil {
				continue
			}
			// Skip the scorer's own most-recent BallHit and the
			// teammate's previous BallHit — those are expected and
			// don't break the chain. Any OTHER player's BallHit
			// (an opponent, or another teammate later than the
			// shooting one) means the teammate's earlier Shot
			// can't be the one that became the goal.
			if rec.Player.ID == scorer.ID || rec.Player.ID == teammate.ID {
				continue
			}
			return false
		case "StatfeedEvent":
			rec, ok := m.Payload.(*types.StatfeedRecord)
			if !ok || rec == nil || rec.EventName != "Shot" {
				continue
			}
			if !sameShortcutPlayer(rec.MainRef, teammateRef) {
				continue
			}
			c.RemoveByName("StatfeedEvent", func(p interface{}) bool {
				r, ok := p.(*types.StatfeedRecord)
				if !ok || r == nil {
					return false
				}
				return r.EventName == "Shot" && sameShortcutPlayer(r.MainRef, teammateRef)
			})
			return true
		}
	}
	return false
}
