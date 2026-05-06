package main

import (
	"encoding/json"
	"sync"
)

// realGoalsLookup is the slim interface StatfeedEmitter needs from
// the goal-emitting processor. RL's HatTrick statfeed counts own
// goals toward the threshold; we don't, so _HatTrick is suppressed
// when the player's tracked real-goal count is below 3.
//
// Implemented today by the legacy Synthesizer (which still owns
// onGoalScored). When Goal extracts and OwnGoalEmitter takes over
// realGoalsByID per the spec, that processor will satisfy the
// interface instead — no code change needed here.
type realGoalsLookup interface {
	RealGoals(playerID string) int
}

// StatfeedEmitter owns every Statfeed-driven synthetic event:
//
//   - The catch-all _StatfeedEvent (every Statfeed variant, with
//     resolved targets).
//   - The promoted dedicated events: _Save, _EpicSave, _Shot, _Assist,
//     _Center, _Clear, _BicycleHit, _FlipReset, _HatTrick,
//     _PlayerDemolished, _DemoChain.
//   - _UnknownStatfeed for any variant not in the verified registry.
//
// State owned:
//
//   - flipResetArmed: scorer was airborne with a flip reset; consumed
//     by the goal emitter for the IsFlipResetGoal modifier flag.
//   - flipResetCountByID: per-match counter on every _FlipReset wire
//     payload.
//
// State read from shared stores:
//
//   - roster (resolve targets)
//   - correlation (lookback for Save→Shot, Assist→Goal,
//     touch-variants→BallHit)
//   - realGoals (HatTrick suppression)
//
// Demos are split into a typed _Demolish event the DemosEmitter
// consumes; this emitter doesn't own _PlayerDemolished or _DemoChain.
//
// Dispatch is idempotent across reset events: MatchCreated and
// MatchDestroyed both clear per-match maps so a back-to-back rejoin
// into the same lobby starts fresh.
type StatfeedEmitter struct {
	roster      *RosterTracker
	correlation *CorrelationBuffer
	discoveries *StatfeedDiscoveryStore
	realGoals   realGoalsLookup

	// flipResetMu guards flipResetArmed + flipResetCountByID. Both
	// are written from Process (FlipReset variant) and read from
	// Process (Goal lookup via ConsumeFlipResetArm). Pipeline.Run is
	// single-threaded so the lock is precautionary — there's no
	// concurrent Process today, but ConsumeFlipResetArm is a public
	// method called from a different processor's Process.
	flipResetMu        sync.Mutex
	flipResetArmed     map[string]bool
	flipResetCountByID map[string]int
}

func NewStatfeedEmitter(
	roster *RosterTracker,
	correlation *CorrelationBuffer,
	discoveries *StatfeedDiscoveryStore,
	realGoals realGoalsLookup,
) *StatfeedEmitter {
	return &StatfeedEmitter{
		roster:             roster,
		correlation:        correlation,
		discoveries:        discoveries,
		realGoals:          realGoals,
		flipResetArmed:     make(map[string]bool),
		flipResetCountByID: make(map[string]int),
	}
}

// ConsumeFlipResetArm returns whether `playerID` had a flip reset
// armed and clears the flag. Called by GoalEmitter (currently the
// legacy synth's onGoalScored) when stamping IsFlipResetGoal.
func (e *StatfeedEmitter) ConsumeFlipResetArm(playerID string) bool {
	if playerID == "" {
		return false
	}
	e.flipResetMu.Lock()
	defer e.flipResetMu.Unlock()
	armed := e.flipResetArmed[playerID]
	if armed {
		delete(e.flipResetArmed, playerID)
	}
	return armed
}

// ClearFlipResetArm drops a player's armed flag without consuming it
// as a goal modifier. Called from the tick-diff path when the player
// touches ground.
func (e *StatfeedEmitter) ClearFlipResetArm(playerID string) {
	if playerID == "" {
		return
	}
	e.flipResetMu.Lock()
	defer e.flipResetMu.Unlock()
	delete(e.flipResetArmed, playerID)
}

// Process handles MatchCreated/MatchDestroyed (reset) and StatfeedEvent
// (the actual work). Everything else is ignored.
func (e *StatfeedEmitter) Process(evt Event) []Event {
	switch evt.Name {
	case "MatchCreated", "MatchDestroyed":
		e.reset()
		return nil
	case "StatfeedEvent":
		return e.processStatfeed(evt)
	}
	return nil
}

func (e *StatfeedEmitter) reset() {
	e.flipResetMu.Lock()
	for k := range e.flipResetArmed {
		delete(e.flipResetArmed, k)
	}
	for k := range e.flipResetCountByID {
		delete(e.flipResetCountByID, k)
	}
	e.flipResetMu.Unlock()
}

func (e *StatfeedEmitter) processStatfeed(evt Event) []Event {
	inner := unwrapInnerData(evt.Raw)
	if inner == "" {
		return nil
	}
	var d statfeedData
	if err := json.Unmarshal([]byte(inner), &d); err != nil {
		return nil
	}
	guid := pickStr(d.MatchGUID, d.MatchGUIDLow)
	eventName := d.EventName
	if eventName == "" {
		eventName = d.EventNameLow
	}
	typeStr := d.Type
	if typeStr == "" {
		typeStr = d.TypeLow
	}
	main := d.MainTarget
	if main == nil {
		main = d.MainTargetLow
	}
	secondary := d.SecondaryTarget
	if secondary == nil {
		secondary = d.SecondTargetLow
	}

	var resolvedMain, resolvedSecondary *EnrichedPlayer
	if main != nil {
		resolvedMain = e.roster.ResolveByShortcut(*main)
	}
	if secondary != nil {
		resolvedSecondary = e.roster.ResolveByShortcut(*secondary)
	}

	// Record into the correlation buffer so _GoalScored / Phase-3
	// events can look back for same-frame modifiers. Stash the wire
	// ref for shortcut-matching plus the resolved enrichment for
	// forward use.
	e.correlation.Record("StatfeedEvent", &statfeedRecord{
		EventName: eventName,
		MainRef:   main,
		Resolved:  resolvedMain,
	})

	// Catch-all _StatfeedEvent first — same wire shape every
	// subscriber depends on, irrespective of variant promotion.
	out := make([]Event, 0, 3)
	if body, err := json.Marshal(struct {
		MatchGUID       string          `json:"matchGuid,omitempty"`
		EventName       string          `json:"eventName"`
		Type            string          `json:"type,omitempty"`
		MainTarget      *EnrichedPlayer `json:"mainTarget,omitempty"`
		SecondaryTarget *EnrichedPlayer `json:"secondaryTarget,omitempty"`
	}{
		MatchGUID:       guid,
		EventName:       eventName,
		Type:            typeStr,
		MainTarget:      resolvedMain,
		SecondaryTarget: resolvedSecondary,
	}); err == nil {
		out = append(out, Event{Name: "_StatfeedEvent", Data: body})
	}

	// Variant fan-out. Unknown names land as _UnknownStatfeed plus a
	// discoveries-store bump.
	if _, known := verifiedStatfeedNames[eventName]; !known {
		if u := e.unknownStatfeed(guid, eventName, resolvedMain, resolvedSecondary); u != nil {
			out = append(out, *u)
		}
	}
	switch eventName {
	case "Demolish":
		// Typed _Demolish carries just the resolved targets. DemosEmitter
		// consumes it and produces _PlayerDemolished (with attackerSpeed)
		// and _DemoChain (with chain detection).
		if resolvedMain != nil && resolvedSecondary != nil {
			body, err := json.Marshal(struct {
				MatchGUID string          `json:"matchGuid,omitempty"`
				Attacker  *EnrichedPlayer `json:"attacker"`
				Victim    *EnrichedPlayer `json:"victim"`
			}{
				MatchGUID: guid,
				Attacker:  resolvedMain,
				Victim:    resolvedSecondary,
			})
			if err == nil {
				out = append(out, Event{Name: "_Demolish", Data: body})
			}
		}
	case "FlipReset":
		if v := e.flipReset(guid, resolvedMain); v != nil {
			out = append(out, *v)
		}
	case "HatTrick":
		if v := e.hatTrick(guid, resolvedMain); v != nil {
			out = append(out, *v)
		}
	case "Save":
		if v := e.save(guid, resolvedMain, "_Save"); v != nil {
			out = append(out, *v)
		}
	case "EpicSave":
		if v := e.save(guid, resolvedMain, "_EpicSave"); v != nil {
			out = append(out, *v)
		}
	case "Shot":
		if v := e.shot(guid, resolvedMain); v != nil {
			out = append(out, *v)
		}
	case "Assist":
		if v := e.assist(guid, resolvedMain); v != nil {
			out = append(out, *v)
		}
	case "Center":
		if v := e.touchVariant(guid, resolvedMain, "_Center"); v != nil {
			out = append(out, *v)
		}
	case "Clear":
		if v := e.touchVariant(guid, resolvedMain, "_Clear"); v != nil {
			out = append(out, *v)
		}
	case "BicycleHit":
		if v := e.touchVariant(guid, resolvedMain, "_BicycleHit"); v != nil {
			out = append(out, *v)
		}
	}
	return out
}

func (e *StatfeedEmitter) unknownStatfeed(guid, eventName string, main, secondary *EnrichedPlayer) *Event {
	body, err := json.Marshal(struct {
		MatchGUID       string          `json:"matchGuid,omitempty"`
		EventName       string          `json:"eventName"`
		MainTarget      *EnrichedPlayer `json:"mainTarget,omitempty"`
		SecondaryTarget *EnrichedPlayer `json:"secondaryTarget,omitempty"`
	}{
		MatchGUID:       guid,
		EventName:       eventName,
		MainTarget:      main,
		SecondaryTarget: secondary,
	})
	if err != nil {
		return nil
	}
	if e.discoveries != nil {
		e.discoveries.Record(eventName)
	}
	return &Event{Name: "_UnknownStatfeed", Data: body}
}

func (e *StatfeedEmitter) flipReset(guid string, main *EnrichedPlayer) *Event {
	if main == nil {
		return nil
	}
	count := 0
	if main.ID != "" {
		e.flipResetMu.Lock()
		e.flipResetArmed[main.ID] = true
		e.flipResetCountByID[main.ID]++
		count = e.flipResetCountByID[main.ID]
		e.flipResetMu.Unlock()
	}
	body, err := json.Marshal(struct {
		MatchGUID           string          `json:"matchGuid,omitempty"`
		MainTarget          *EnrichedPlayer `json:"mainTarget"`
		FlipResetsThisMatch int             `json:"flipResetsThisMatch"`
	}{
		MatchGUID:           guid,
		MainTarget:          main,
		FlipResetsThisMatch: count,
	})
	if err != nil {
		return nil
	}
	return &Event{Name: "_FlipReset", Data: body}
}

func (e *StatfeedEmitter) hatTrick(guid string, main *EnrichedPlayer) *Event {
	if main == nil {
		return nil
	}
	if e.realGoals == nil || e.realGoals.RealGoals(main.ID) < 3 {
		// RL's HatTrick statfeed counts own goals; we don't. Suppress
		// the event when the real-goal count is below 3.
		return nil
	}
	body, err := json.Marshal(struct {
		MatchGUID      string          `json:"matchGuid,omitempty"`
		MainTarget     *EnrichedPlayer `json:"mainTarget"`
		GoalsThisMatch int             `json:"goalsThisMatch"`
	}{
		MatchGUID:      guid,
		MainTarget:     main,
		GoalsThisMatch: e.realGoals.RealGoals(main.ID),
	})
	if err != nil {
		return nil
	}
	return &Event{Name: "_HatTrick", Data: body}
}

func (e *StatfeedEmitter) save(guid string, main *EnrichedPlayer, eventName string) *Event {
	if main == nil {
		return nil
	}
	// Look back for a recent Shot statfeed by an opposing-team
	// player. Saves can land several events after the shot.
	var correlatedShot *EnrichedPlayer
	for _, p := range e.correlation.Recent("StatfeedEvent", 15) {
		rec, ok := p.(*statfeedRecord)
		if !ok || rec.EventName != "Shot" {
			continue
		}
		if rec.Resolved == nil || rec.Resolved.Team == main.Team {
			continue
		}
		correlatedShot = rec.Resolved
		break
	}
	body, err := json.Marshal(struct {
		MatchGUID      string          `json:"matchGuid,omitempty"`
		MainTarget     *EnrichedPlayer `json:"mainTarget"`
		CorrelatedShot *EnrichedPlayer `json:"correlatedShot,omitempty"`
	}{
		MatchGUID:      guid,
		MainTarget:     main,
		CorrelatedShot: correlatedShot,
	})
	if err != nil {
		return nil
	}
	return &Event{Name: eventName, Data: body}
}

func (e *StatfeedEmitter) shot(guid string, main *EnrichedPlayer) *Event {
	if main == nil {
		return nil
	}
	body, err := json.Marshal(struct {
		MatchGUID       string                   `json:"matchGuid,omitempty"`
		MainTarget      *EnrichedPlayer          `json:"mainTarget"`
		CorrelatedTouch *enrichedCorrelatedTouch `json:"correlatedTouch,omitempty"`
	}{
		MatchGUID:       guid,
		MainTarget:      main,
		CorrelatedTouch: recentTouch(e.correlation, 3),
	})
	if err != nil {
		return nil
	}
	return &Event{Name: "_Shot", Data: body}
}

func (e *StatfeedEmitter) assist(guid string, main *EnrichedPlayer) *Event {
	if main == nil {
		return nil
	}
	var correlatedGoal *goalRecord
	for _, p := range e.correlation.Recent("_GoalScored", 5) {
		if g, ok := p.(*goalRecord); ok {
			correlatedGoal = g
			break
		}
	}
	type goalRef struct {
		Scorer        *EnrichedPlayer `json:"scorer,omitempty"`
		ScoringTeam   int             `json:"scoringTeam"`
		ConcedingTeam int             `json:"concedingTeam"`
	}
	var goalSummary *goalRef
	if correlatedGoal != nil {
		goalSummary = &goalRef{
			Scorer:        correlatedGoal.Scorer,
			ScoringTeam:   correlatedGoal.ScoringTeam,
			ConcedingTeam: correlatedGoal.ConcedingTeam,
		}
	}
	body, err := json.Marshal(struct {
		MatchGUID      string          `json:"matchGuid,omitempty"`
		MainTarget     *EnrichedPlayer `json:"mainTarget"`
		CorrelatedGoal *goalRef        `json:"correlatedGoal,omitempty"`
	}{
		MatchGUID:      guid,
		MainTarget:     main,
		CorrelatedGoal: goalSummary,
	})
	if err != nil {
		return nil
	}
	return &Event{Name: "_Assist", Data: body}
}

func (e *StatfeedEmitter) touchVariant(guid string, main *EnrichedPlayer, eventName string) *Event {
	if main == nil {
		return nil
	}
	body, err := json.Marshal(struct {
		MatchGUID       string                   `json:"matchGuid,omitempty"`
		MainTarget      *EnrichedPlayer          `json:"mainTarget"`
		CorrelatedTouch *enrichedCorrelatedTouch `json:"correlatedTouch,omitempty"`
	}{
		MatchGUID:       guid,
		MainTarget:      main,
		CorrelatedTouch: recentTouch(e.correlation, 3),
	})
	if err != nil {
		return nil
	}
	return &Event{Name: eventName, Data: body}
}

// recentTouch looks up the most recent BallHit from the shared
// correlation buffer and returns it as a wire-ready
// enrichedCorrelatedTouch. Free function so emit_goal.go can use it
// too without depending on StatfeedEmitter.
func recentTouch(correlation *CorrelationBuffer, window int) *enrichedCorrelatedTouch {
	for _, p := range correlation.Recent("BallHit", window) {
		rec, ok := p.(*ballHitRecord)
		if !ok || rec == nil || rec.Player == nil {
			continue
		}
		return &enrichedCorrelatedTouch{
			Player:       rec.Player,
			PreHitSpeed:  rec.PreHitSpeed,
			PostHitSpeed: rec.PostHitSpeed,
		}
	}
	return nil
}
