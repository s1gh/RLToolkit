package backend

import (
	"encoding/json"
	"testing"
)

func TestOwnGoalEmitter_FiresOnDeflection(t *testing.T) {
	matchState := liveMatchState(t)
	ticks := NewTickStore()
	correlation := NewCorrelationBuffer(8)
	correlation.Record("BallHit", &ballHitRecord{
		Player: &EnrichedPlayer{ID: "Steam|2|0", Name: "Defender", Team: 1},
	})
	e := NewOwnGoalEmitter(matchState, ticks, correlation)

	ticks.Observe(updateStateTick(t, "G1", 0, 0, false))
	if got := e.Process(updateStateTick(t, "G1", 0, 0, false)); len(got) != 0 {
		t.Fatalf("no score delta should not fire, got %v", got)
	}

	// Blue (team 0) score goes up by 1; last touch was by team 1 — own goal.
	ticks.Observe(updateStateTick(t, "G1", 1, 0, false))
	out := e.Process(updateStateTick(t, "G1", 1, 0, false))
	if len(out) != 1 || out[0].Name != "_OwnGoal" {
		t.Fatalf("expected one _OwnGoal, got %v", out)
	}
	var payload struct {
		Deflector     *EnrichedPlayer `json:"deflector"`
		ScoringTeam   int             `json:"scoringTeam"`
		ConcedingTeam int             `json:"concedingTeam"`
	}
	if err := json.Unmarshal(out[0].Data, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload.Deflector == nil || payload.Deflector.ID != "Steam|2|0" {
		t.Fatalf("deflector not stamped: %+v", payload.Deflector)
	}
	if payload.ScoringTeam != 0 || payload.ConcedingTeam != 1 {
		t.Fatalf("teams wrong: scoring=%d conceding=%d", payload.ScoringTeam, payload.ConcedingTeam)
	}
}

func TestOwnGoalEmitter_NoFireWhenDeflectorOnScoringTeam(t *testing.T) {
	matchState := liveMatchState(t)
	ticks := NewTickStore()
	correlation := NewCorrelationBuffer(8)
	// Blue scored, last touch was by a Blue player — clean goal, not OG.
	correlation.Record("BallHit", &ballHitRecord{
		Player: &EnrichedPlayer{ID: "Steam|7|0", Name: "Striker", Team: 0},
	})
	e := NewOwnGoalEmitter(matchState, ticks, correlation)

	ticks.Observe(updateStateTick(t, "G1", 0, 0, false))
	ticks.Observe(updateStateTick(t, "G1", 1, 0, false))
	if got := e.Process(updateStateTick(t, "G1", 1, 0, false)); len(got) != 0 {
		t.Fatalf("clean goal should not fire _OwnGoal, got %v", got)
	}
}

func TestOwnGoalEmitter_PhaseGated(t *testing.T) {
	// Default MatchState is PhaseNone — the emitter must skip.
	matchState := NewMatchState()
	ticks := NewTickStore()
	correlation := NewCorrelationBuffer(8)
	correlation.Record("BallHit", &ballHitRecord{
		Player: &EnrichedPlayer{ID: "Steam|2|0", Team: 1},
	})
	e := NewOwnGoalEmitter(matchState, ticks, correlation)
	ticks.Observe(updateStateTick(t, "G1", 0, 0, false))
	ticks.Observe(updateStateTick(t, "G1", 1, 0, false))
	if got := e.Process(updateStateTick(t, "G1", 1, 0, false)); len(got) != 0 {
		t.Fatalf("PhaseNone should skip, got %v", got)
	}
}

func TestOwnGoalEmitter_NoFireOnFirstTick(t *testing.T) {
	e := NewOwnGoalEmitter(liveMatchState(t), NewTickStore(), NewCorrelationBuffer(8))
	out := e.Process(updateStateTick(t, "G1", 0, 0, false))
	if len(out) != 0 {
		t.Fatalf("first tick has no baseline, should not fire, got %v", out)
	}
}

func TestOwnGoalEmitter_DifferentMatchGuid(t *testing.T) {
	matchState := liveMatchState(t)
	ticks := NewTickStore()
	correlation := NewCorrelationBuffer(8)
	correlation.Record("BallHit", &ballHitRecord{
		Player: &EnrichedPlayer{ID: "Steam|2|0", Team: 1},
	})
	e := NewOwnGoalEmitter(matchState, ticks, correlation)
	ticks.Observe(updateStateTick(t, "G1", 0, 0, false))
	// Match boundary — guid changes, score happens to be higher in
	// the new match. Must not be misread as a goal.
	ticks.Observe(updateStateTick(t, "G2", 1, 0, false))
	if got := e.Process(updateStateTick(t, "G2", 1, 0, false)); len(got) != 0 {
		t.Fatalf("guid change should reset baseline, got %v", got)
	}
}

// liveMatchState returns a MatchState already advanced to PhaseLive
// so the emitter's phase gate doesn't trip.
func liveMatchState(t *testing.T) *MatchState {
	t.Helper()
	ms := NewMatchState()
	ms.Observe(Event{Name: "MatchCreated", Data: json.RawMessage(`{"matchGuid":"G1"}`)})
	ms.Observe(Event{Name: "RoundStarted"})
	if ms.Snapshot().Phase != PhaseLive {
		t.Fatalf("setup: expected PhaseLive, got %v", ms.Snapshot().Phase)
	}
	return ms
}
