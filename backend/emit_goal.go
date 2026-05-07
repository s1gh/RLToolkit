package backend

import (
	"encoding/json"
	"rl-toolkit/internal/bus"
	"sync"
)

// goalScoredData mirrors the wire shape of GoalScored.
type goalScoredData struct {
	MatchGUID         string         `json:"MatchGuid"`
	MatchGUIDLow      string         `json:"matchguid"`
	Scorer            *ShortcutRef   `json:"Scorer"`
	ScorerLow         *ShortcutRef   `json:"scorer"`
	Assister          *ShortcutRef   `json:"Assister"`
	AssisterLow       *ShortcutRef   `json:"assister"`
	BallLastTouch     *ballLastTouch `json:"BallLastTouch"`
	BallLastTouchLow  *ballLastTouch `json:"balllasttouch"`
	GoalSpeed         *float64       `json:"GoalSpeed"`
	GoalSpeedLow      *float64       `json:"goalspeed"`
	GoalTime          *float64       `json:"GoalTime"`
	GoalTimeLow       *float64       `json:"goaltime"`
	ImpactLocation    *vec3          `json:"ImpactLocation"`
	ImpactLocationLow *vec3          `json:"impactlocation"`
}

type enrichedGoalScored struct {
	Event          string                 `json:"Event"`
	MatchGUID      string                 `json:"matchGuid,omitempty"`
	Scorer         *EnrichedPlayer        `json:"scorer,omitempty"`
	Assister       *EnrichedPlayer        `json:"assister,omitempty"`
	BallLastTouch  *enrichedBallLastTouch `json:"ballLastTouch,omitempty"`
	GoalSpeed      *float64               `json:"goalSpeed,omitempty"`
	GoalTime       *float64               `json:"goalTime,omitempty"`
	ImpactLocation *vec3                  `json:"impactLocation,omitempty"`
	ScoringTeam    *int                   `json:"scoringTeam,omitempty"`
	ConcedingTeam  *int                   `json:"concedingTeam,omitempty"`
	IsOwnGoal      bool                   `json:"isOwnGoal"`
	Modifiers      *goalModifiers         `json:"modifiers"`
}

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
func sameShortcutPlayer(a, b *ShortcutRef) bool {
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

// flipResetConsumer is the slim interface GoalEmitter needs from
// StatfeedEmitter to consume the per-player flip-reset arm flag for
// the IsFlipResetGoal modifier.
type flipResetConsumer interface {
	ConsumeFlipResetArm(playerID string) bool
}

// goalCounter is the slim interface GoalEmitter needs from
// OwnGoalEmitter for the per-player honest-goal counter (used to
// suppress RL's HatTrick statfeed when the threshold was reached
// only because RL counts own goals).
type goalCounter interface {
	RealGoals(playerID string) int
	BumpRealGoals(playerID string)
}

// GoalEmitter publishes _GoalScored (with modifiers + last-touch +
// own-goal heuristic) and _GoalReplayStarted (on the bReplay rising
// edge during a goal celebration).
//
// Reads:
//   - roster — resolve scorer/assister/ballLastTouch refs.
//   - correlation — same-frame statfeed modifiers; fallback for
//     missing BallLastTouch via Recent("BallHit", 1).
//   - tickStore — bReplay edge detection for _GoalReplayStarted.
//   - flipReset — consume the per-player arm for IsFlipResetGoal.
//   - goals — bump on non-own-goal, look up for HatTrick suppression.
//
// Writes:
//   - correlation: Records ("_GoalScored", goalRecord) so OwnGoal /
//     Statfeed (Assist) can correlate.
type GoalEmitter struct {
	roster      *RosterTracker
	correlation *CorrelationBuffer
	ticks       *TickStore
	flipReset   flipResetConsumer
	goals       goalCounter

	mu         sync.Mutex
	lastGoal   *enrichedGoalScored
	prevReplay bool
}

func NewGoalEmitter(
	roster *RosterTracker,
	correlation *CorrelationBuffer,
	ticks *TickStore,
	flipReset flipResetConsumer,
	goals goalCounter,
) *GoalEmitter {
	return &GoalEmitter{
		roster:      roster,
		correlation: correlation,
		ticks:       ticks,
		flipReset:   flipReset,
		goals:       goals,
	}
}

func (e *GoalEmitter) Process(evt bus.Event) []bus.Event {
	switch evt.Name {
	case "MatchCreated", "MatchDestroyed":
		e.mu.Lock()
		e.lastGoal = nil
		e.prevReplay = false
		e.mu.Unlock()
		return nil
	case "GoalScored":
		if g := e.processGoal(evt); g != nil {
			return []bus.Event{*g}
		}
		return nil
	case "UpdateState":
		return e.maybeReplayStarted()
	}
	return nil
}

func (e *GoalEmitter) processGoal(evt bus.Event) *bus.Event {
	inner := unwrapInnerData(evt.Raw)
	if inner == "" {
		return nil
	}
	var d goalScoredData
	if err := json.Unmarshal([]byte(inner), &d); err != nil {
		return nil
	}

	scorerRef := d.Scorer
	if scorerRef == nil {
		scorerRef = d.ScorerLow
	}
	// SDK guard: drop GoalScored events with no identifiable scorer.
	// RL re-fires GoalScored at round-restart with empty Scorer.Name;
	// without this, a synthetic event with name "Unknown" overwrites
	// the prior tick's scorer for any plugin that caches the latest
	// goal.
	if scorerRef == nil || scorerRef.Name == "" {
		return nil
	}
	scorer := e.roster.ResolveByShortcut(*scorerRef)
	if scorer == nil {
		return nil
	}

	guid := pickStr(d.MatchGUID, d.MatchGUIDLow)
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
		Event:      "_GoalScored",
		MatchGUID:      guid,
		Scorer:         scorer,
		GoalSpeed:      pickFloat(d.GoalSpeed, d.GoalSpeedLow),
		GoalTime:       pickFloat(d.GoalTime, d.GoalTimeLow),
		ImpactLocation: loc,
		ScoringTeam:    &scoringTeam,
		ConcedingTeam:  &concedingTeam,
	}

	if assisterRef != nil && assisterRef.Name != "" {
		out.Assister = e.roster.ResolveByShortcut(*assisterRef)
	}

	var lastToucher *EnrichedPlayer
	if lastTouch != nil {
		ref := lastTouch.Player
		if ref == nil {
			ref = lastTouch.PlayerLow
		}
		sp := pickFloat(lastTouch.Speed, lastTouch.SpeedLow)
		if ref != nil {
			lastToucher = e.roster.ResolveByShortcut(*ref)
		}
		if lastToucher != nil || sp != nil {
			out.BallLastTouch = &enrichedBallLastTouch{
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
			if rec, ok := p.(*ballHitRecord); ok && rec != nil {
				lastToucher = rec.Player
			}
		}
	}
	// Own-goal heuristic. Two shapes RL uses:
	//
	//  (A) Multi-player: RL credits an opposing-team player as Scorer
	//      and the deflector is in BallLastTouch. lastToucher.Team ==
	//      concedingTeam (i.e., the deflector's team is the one that
	//      conceded), so flag it.
	//
	//  (B) Solo / no opposing players: RL credits the deflector
	//      themselves as Scorer (no one else to credit). scorer.Team
	//      == lastToucher.Team and scoringTeam came out wrong (it
	//      should be the *opposing* team that gained the +1). Flip
	//      scoringTeam/concedingTeam to reflect the actual score
	//      change and flag the goal.
	//
	// The richer _OwnGoal event ships via emit_own_goal.go with
	// score-delta verification; this flag is the cheap header.
	if lastToucher != nil {
		if lastToucher.Team == concedingTeam {
			out.IsOwnGoal = true
		} else if lastToucher.Team == scoringTeam && lastToucher.ID == scorer.ID {
			out.IsOwnGoal = true
			scoringTeam, concedingTeam = concedingTeam, scoringTeam
			out.ScoringTeam = &scoringTeam
			out.ConcedingTeam = &concedingTeam
		}
	}

	// Bump the per-player real-goal counter for non-own-goals so
	// _HatTrick can verify RL's HatTrick threshold against actual
	// scoring (RL's counter includes own goals; we don't).
	if !out.IsOwnGoal && e.goals != nil {
		e.goals.BumpRealGoals(scorer.ID)
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
	out.Modifiers = mods

	// Record the resolved goal so _OwnGoal can attach it as
	// `correlatedGoal` and _Assist can find its companion.
	e.correlation.Record("_GoalScored", &goalRecord{
		Scorer:        scorer,
		ScoringTeam:   scoringTeam,
		ConcedingTeam: concedingTeam,
	})

	// Cache the full enriched envelope so the bReplay rising edge
	// can mirror it as _GoalReplayStarted. The correlation buffer
	// gets evicted by burst BallHit/StatfeedEvent traffic during a
	// celebration, so we keep our own copy.
	cached := out
	e.mu.Lock()
	e.lastGoal = &cached
	e.mu.Unlock()

	body, err := marshalGoalBody(out)
	if err != nil {
		return nil
	}
	return &bus.Event{Name: "_GoalScored", Data: body}
}

func (e *GoalEmitter) maybeReplayStarted() []bus.Event {
	curr := e.ticks.Latest()
	if curr == nil {
		return nil
	}
	e.mu.Lock()
	prev := e.prevReplay
	e.prevReplay = curr.bReplay
	cached := e.lastGoal
	e.mu.Unlock()
	if prev || !curr.bReplay || cached == nil {
		return nil
	}
	out := *cached
	out.Event = "_GoalReplayStarted"
	out.MatchGUID = curr.matchGUID
	body, err := marshalGoalBody(out)
	if err != nil {
		return nil
	}
	return []bus.Event{{Name: "_GoalReplayStarted", Data: body}}
}

// marshalGoalBody encodes an enrichedGoalScored as just its data
// payload (no top-level Event field — the bus envelope provides
// that). The legacy synth emitted the Event field inline; the new
// pipeline wraps via the Bus.Broadcast envelope path.
func marshalGoalBody(g enrichedGoalScored) ([]byte, error) {
	return json.Marshal(struct {
		MatchGUID      string                 `json:"matchGuid,omitempty"`
		Scorer         *EnrichedPlayer        `json:"scorer,omitempty"`
		Assister       *EnrichedPlayer        `json:"assister,omitempty"`
		BallLastTouch  *enrichedBallLastTouch `json:"ballLastTouch,omitempty"`
		GoalSpeed      *float64               `json:"goalSpeed,omitempty"`
		GoalTime       *float64               `json:"goalTime,omitempty"`
		ImpactLocation *vec3                  `json:"impactLocation,omitempty"`
		ScoringTeam    *int                   `json:"scoringTeam,omitempty"`
		ConcedingTeam  *int                   `json:"concedingTeam,omitempty"`
		IsOwnGoal      bool                   `json:"isOwnGoal"`
		Modifiers      *goalModifiers         `json:"modifiers,omitempty"`
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
func collectGoalModifiers(correlation *CorrelationBuffer, scorer *ShortcutRef) *goalModifiers {
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
	for _, p := range correlation.Recent("StatfeedEvent", 15) {
		rec, ok := p.(*statfeedRecord)
		if !ok || rec.MainRef == nil {
			continue
		}
		if !sameShortcutPlayer(rec.MainRef, scorer) {
			continue
		}
		apply(rec.EventName)
	}

	// Consume modifier statfeeds so they don't apply to the next goal.
	correlation.RemoveByName("StatfeedEvent", func(p interface{}) bool {
		rec, ok := p.(*statfeedRecord)
		if !ok || rec.MainRef == nil {
			return false
		}
		_, isModifier := modifierStatfeedNames[rec.EventName]
		return isModifier && sameShortcutPlayer(rec.MainRef, scorer)
	})

	return mods
}
