package emit

import (
	"encoding/json"
	"rl-toolkit/backend/internal/bus"
	"rl-toolkit/backend/internal/types"
	"rl-toolkit/backend/internal/wire"
	"sync"
)

// Statfeed owns every Statfeed-driven synthetic event: the catch-all
// _StatfeedEvent, the promoted dedicated events (_Save, _EpicSave,
// _Shot, _Assist, _Center, _Clear, _BicycleHit, _FlipReset, _HatTrick,
// _Demolish), and _UnknownStatfeed for variants not in the verified
// registry.
//
// Owns flipResetArmed (consumed by Goal for IsFlipResetGoal) and
// flipResetCountByID (per-match counter on _FlipReset). Reads roster,
// correlation lookback, realGoals (HatTrick suppression) and
// discoveries (unknown-variant recording).
//
// _Demolish is consumed by the Demos emitter, which produces
// _PlayerDemolished and _DemoChain. Per-match maps are cleared on
// MatchCreated and MatchDestroyed.
type Statfeed struct {
	roster      RosterResolver
	correlation Correlator
	discoveries DiscoveryRecorder
	realGoals   RealGoalsLookup

	flipResetMu        sync.Mutex
	flipResetArmed     map[string]bool
	flipResetCountByID map[string]int
}

func NewStatfeed(
	roster RosterResolver,
	correlation Correlator,
	discoveries DiscoveryRecorder,
	realGoals RealGoalsLookup,
) *Statfeed {
	return &Statfeed{
		roster:             roster,
		correlation:        correlation,
		discoveries:        discoveries,
		realGoals:          realGoals,
		flipResetArmed:     make(map[string]bool),
		flipResetCountByID: make(map[string]int),
	}
}

// ConsumeFlipResetArm returns whether `playerID` had a flip reset
// armed and clears the flag. Called by Goal when stamping
// IsFlipResetGoal.
func (e *Statfeed) ConsumeFlipResetArm(playerID string) bool {
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
func (e *Statfeed) ClearFlipResetArm(playerID string) {
	if playerID == "" {
		return
	}
	e.flipResetMu.Lock()
	defer e.flipResetMu.Unlock()
	delete(e.flipResetArmed, playerID)
}

func (e *Statfeed) Process(evt bus.Event) []bus.Event {
	switch evt.Name {
	case "MatchCreated", "MatchDestroyed":
		e.reset()
		return nil
	case "StatfeedEvent":
		return e.processStatfeed(evt)
	}
	return nil
}

func (e *Statfeed) reset() {
	e.flipResetMu.Lock()
	for k := range e.flipResetArmed {
		delete(e.flipResetArmed, k)
	}
	for k := range e.flipResetCountByID {
		delete(e.flipResetCountByID, k)
	}
	e.flipResetMu.Unlock()
}

func (e *Statfeed) processStatfeed(evt bus.Event) []bus.Event {
	inner := wire.UnwrapInnerData(evt.Raw)
	if inner == "" {
		return nil
	}
	var d types.StatfeedData
	if err := json.Unmarshal([]byte(inner), &d); err != nil {
		return nil
	}
	guid := wire.PickStr(d.MatchGUID, d.MatchGUIDLow)
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

	var resolvedMain, resolvedSecondary *types.EnrichedPlayer
	if main != nil {
		resolvedMain = e.roster.ResolveByShortcut(*main)
	}
	if secondary != nil {
		resolvedSecondary = e.roster.ResolveByShortcut(*secondary)
	}

	// Record into the correlation buffer so downstream emitters can
	// look back for same-frame modifiers. Stashes the wire ref for
	// shortcut-matching plus the resolved enrichment for forward use.
	e.correlation.Record("StatfeedEvent", &types.StatfeedRecord{
		EventName: eventName,
		MainRef:   main,
		Resolved:  resolvedMain,
	})

	// Catch-all _StatfeedEvent first — same wire shape regardless of
	// variant promotion.
	out := make([]bus.Event, 0, 3)
	if body, err := json.Marshal(struct {
		MatchGUID       string                `json:"matchGuid,omitempty"`
		EventName       string                `json:"eventName"`
		Type            string                `json:"type,omitempty"`
		MainTarget      *types.EnrichedPlayer `json:"mainTarget,omitempty"`
		SecondaryTarget *types.EnrichedPlayer `json:"secondaryTarget,omitempty"`
	}{
		MatchGUID:       guid,
		EventName:       eventName,
		Type:            typeStr,
		MainTarget:      resolvedMain,
		SecondaryTarget: resolvedSecondary,
	}); err == nil {
		out = append(out, bus.Event{Name: "_StatfeedEvent", Data: body})
	}

	// Variant fan-out. Unknown names land as _UnknownStatfeed plus a
	// discoveries-store bump.
	if _, known := types.VerifiedStatfeedNames[eventName]; !known {
		if u := e.unknownStatfeed(guid, eventName, resolvedMain, resolvedSecondary); u != nil {
			out = append(out, *u)
		}
	}
	switch eventName {
	case "Demolish":
		// Demos consumes _Demolish and produces _PlayerDemolished (with
		// attackerSpeed) and _DemoChain.
		if resolvedMain != nil && resolvedSecondary != nil {
			body, err := json.Marshal(struct {
				MatchGUID string                `json:"matchGuid,omitempty"`
				Attacker  *types.EnrichedPlayer `json:"attacker"`
				Victim    *types.EnrichedPlayer `json:"victim"`
			}{
				MatchGUID: guid,
				Attacker:  resolvedMain,
				Victim:    resolvedSecondary,
			})
			if err == nil {
				out = append(out, bus.Event{Name: "_Demolish", Data: body})
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

func (e *Statfeed) unknownStatfeed(guid, eventName string, main, secondary *types.EnrichedPlayer) *bus.Event {
	body, err := json.Marshal(struct {
		MatchGUID       string                `json:"matchGuid,omitempty"`
		EventName       string                `json:"eventName"`
		MainTarget      *types.EnrichedPlayer `json:"mainTarget,omitempty"`
		SecondaryTarget *types.EnrichedPlayer `json:"secondaryTarget,omitempty"`
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
	return &bus.Event{Name: "_UnknownStatfeed", Data: body}
}

func (e *Statfeed) flipReset(guid string, main *types.EnrichedPlayer) *bus.Event {
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
		MatchGUID           string                `json:"matchGuid,omitempty"`
		MainTarget          *types.EnrichedPlayer `json:"mainTarget"`
		FlipResetsThisMatch int                   `json:"flipResetsThisMatch"`
	}{
		MatchGUID:           guid,
		MainTarget:          main,
		FlipResetsThisMatch: count,
	})
	if err != nil {
		return nil
	}
	return &bus.Event{Name: "_FlipReset", Data: body}
}

func (e *Statfeed) hatTrick(guid string, main *types.EnrichedPlayer) *bus.Event {
	if main == nil {
		return nil
	}
	// RL's HatTrick statfeed counts own goals; we don't.
	if e.realGoals == nil || e.realGoals.RealGoals(main.ID) < 3 {
		return nil
	}
	body, err := json.Marshal(struct {
		MatchGUID      string                `json:"matchGuid,omitempty"`
		MainTarget     *types.EnrichedPlayer `json:"mainTarget"`
		GoalsThisMatch int                   `json:"goalsThisMatch"`
	}{
		MatchGUID:      guid,
		MainTarget:     main,
		GoalsThisMatch: e.realGoals.RealGoals(main.ID),
	})
	if err != nil {
		return nil
	}
	return &bus.Event{Name: "_HatTrick", Data: body}
}

func (e *Statfeed) save(guid string, main *types.EnrichedPlayer, eventName string) *bus.Event {
	if main == nil {
		return nil
	}
	// Saves can land several events after the shot, so look back for
	// a Shot statfeed by an opposing-team player.
	var correlatedShot *types.EnrichedPlayer
	for _, p := range e.correlation.Recent("StatfeedEvent", 15) {
		rec, ok := p.(*types.StatfeedRecord)
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
		MatchGUID      string                `json:"matchGuid,omitempty"`
		MainTarget     *types.EnrichedPlayer `json:"mainTarget"`
		CorrelatedShot *types.EnrichedPlayer `json:"correlatedShot,omitempty"`
	}{
		MatchGUID:      guid,
		MainTarget:     main,
		CorrelatedShot: correlatedShot,
	})
	if err != nil {
		return nil
	}
	return &bus.Event{Name: eventName, Data: body}
}

func (e *Statfeed) shot(guid string, main *types.EnrichedPlayer) *bus.Event {
	if main == nil {
		return nil
	}
	body, err := json.Marshal(struct {
		MatchGUID       string                         `json:"matchGuid,omitempty"`
		MainTarget      *types.EnrichedPlayer          `json:"mainTarget"`
		CorrelatedTouch *types.EnrichedCorrelatedTouch `json:"correlatedTouch,omitempty"`
	}{
		MatchGUID:       guid,
		MainTarget:      main,
		CorrelatedTouch: recentTouch(e.correlation, 3),
	})
	if err != nil {
		return nil
	}
	return &bus.Event{Name: "_Shot", Data: body}
}

func (e *Statfeed) assist(guid string, main *types.EnrichedPlayer) *bus.Event {
	if main == nil {
		return nil
	}
	var correlatedGoal *types.GoalRecord
	for _, p := range e.correlation.Recent("_GoalScored", 5) {
		if g, ok := p.(*types.GoalRecord); ok {
			correlatedGoal = g
			break
		}
	}
	type goalRef struct {
		Scorer        *types.EnrichedPlayer `json:"scorer,omitempty"`
		ScoringTeam   int                   `json:"scoringTeam"`
		ConcedingTeam int                   `json:"concedingTeam"`
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
		MatchGUID      string                `json:"matchGuid,omitempty"`
		MainTarget     *types.EnrichedPlayer `json:"mainTarget"`
		CorrelatedGoal *goalRef              `json:"correlatedGoal,omitempty"`
	}{
		MatchGUID:      guid,
		MainTarget:     main,
		CorrelatedGoal: goalSummary,
	})
	if err != nil {
		return nil
	}
	return &bus.Event{Name: "_Assist", Data: body}
}

func (e *Statfeed) touchVariant(guid string, main *types.EnrichedPlayer, eventName string) *bus.Event {
	if main == nil {
		return nil
	}
	body, err := json.Marshal(struct {
		MatchGUID       string                         `json:"matchGuid,omitempty"`
		MainTarget      *types.EnrichedPlayer          `json:"mainTarget"`
		CorrelatedTouch *types.EnrichedCorrelatedTouch `json:"correlatedTouch,omitempty"`
	}{
		MatchGUID:       guid,
		MainTarget:     main,
		CorrelatedTouch: recentTouch(e.correlation, 3),
	})
	if err != nil {
		return nil
	}
	return &bus.Event{Name: eventName, Data: body}
}
