package emit

import (
	"encoding/json"
	"rl-toolkit/internal/bus"
	"rl-toolkit/internal/correlation"
	"rl-toolkit/internal/tick"
	"rl-toolkit/internal/types"
	"testing"
)

func livePhase() PhaseGate { return stubPhase{phase: types.PhaseLive} }

func TestOwnGoal_FiresOnDeflection(t *testing.T) {
	ticks := tick.New()
	corr := correlation.New(8)
	corr.Record("BallHit", &types.BallHitRecord{
		Player: &types.EnrichedPlayer{ID: "Steam|2|0", Name: "Defender", Team: 1},
	})
	e := NewOwnGoal(livePhase(), ticks, corr)

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
		Deflector     *types.EnrichedPlayer `json:"deflector"`
		ScoringTeam   int                   `json:"scoringTeam"`
		ConcedingTeam int                   `json:"concedingTeam"`
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

func TestOwnGoal_NoFireWhenDeflectorOnScoringTeam(t *testing.T) {
	ticks := tick.New()
	corr := correlation.New(8)
	// Blue scored, last touch was by a Blue player — clean goal, not OG.
	corr.Record("BallHit", &types.BallHitRecord{
		Player: &types.EnrichedPlayer{ID: "Steam|7|0", Name: "Striker", Team: 0},
	})
	e := NewOwnGoal(livePhase(), ticks, corr)

	ticks.Observe(updateStateTick(t, "G1", 0, 0, false))
	ticks.Observe(updateStateTick(t, "G1", 1, 0, false))
	if got := e.Process(updateStateTick(t, "G1", 1, 0, false)); len(got) != 0 {
		t.Fatalf("clean goal should not fire _OwnGoal, got %v", got)
	}
}

func TestOwnGoal_PhaseGated(t *testing.T) {
	ticks := tick.New()
	corr := correlation.New(8)
	corr.Record("BallHit", &types.BallHitRecord{
		Player: &types.EnrichedPlayer{ID: "Steam|2|0", Team: 1},
	})
	e := NewOwnGoal(stubPhase{phase: types.PhaseNone}, ticks, corr)
	ticks.Observe(updateStateTick(t, "G1", 0, 0, false))
	ticks.Observe(updateStateTick(t, "G1", 1, 0, false))
	if got := e.Process(updateStateTick(t, "G1", 1, 0, false)); len(got) != 0 {
		t.Fatalf("PhaseNone should skip, got %v", got)
	}
}

func TestOwnGoal_NoFireOnFirstTick(t *testing.T) {
	e := NewOwnGoal(livePhase(), tick.New(), correlation.New(8))
	out := e.Process(updateStateTick(t, "G1", 0, 0, false))
	if len(out) != 0 {
		t.Fatalf("first tick has no baseline, should not fire, got %v", out)
	}
}

func TestOwnGoal_DifferentMatchGuid(t *testing.T) {
	ticks := tick.New()
	corr := correlation.New(8)
	corr.Record("BallHit", &types.BallHitRecord{
		Player: &types.EnrichedPlayer{ID: "Steam|2|0", Team: 1},
	})
	e := NewOwnGoal(livePhase(), ticks, corr)
	ticks.Observe(updateStateTick(t, "G1", 0, 0, false))
	// Match boundary — guid changes, score happens to be higher in
	// the new match. Must not be misread as a goal.
	ticks.Observe(updateStateTick(t, "G2", 1, 0, false))
	if got := e.Process(updateStateTick(t, "G2", 1, 0, false)); len(got) != 0 {
		t.Fatalf("guid change should reset baseline, got %v", got)
	}
}

// updateStateTick builds a minimal UpdateState envelope. Local copy
// for the emit-package tests so we don't reach across packages.
func updateStateTick(t *testing.T, guid string, scoreBlue, scoreOrange int, bReplay bool) bus.Event {
	t.Helper()
	inner, _ := json.Marshal(map[string]any{
		"MatchGuid": guid,
		"Game": map[string]any{
			"bReplay": bReplay,
			"Teams":   []map[string]any{{"TeamNum": 0, "Score": scoreBlue}, {"TeamNum": 1, "Score": scoreOrange}},
		},
		"Players": []map[string]any{},
	})
	raw, _ := json.Marshal(map[string]any{"Event": "UpdateState", "Data": string(inner)})
	return bus.Event{Name: "UpdateState", Raw: raw}
}
