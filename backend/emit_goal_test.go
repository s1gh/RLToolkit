package main

import (
	"encoding/json"
	"testing"
)

// fakeFlipReset is a no-op flipResetConsumer for tests that don't
// exercise the IsFlipResetGoal path.
type fakeFlipReset struct{}

func (f *fakeFlipReset) ConsumeFlipResetArm(string) bool { return false }

// fakeGoalCounter is the tiny in-memory goalCounter used for goal tests.
type fakeGoalCounter struct{ counts map[string]int }

func (f *fakeGoalCounter) RealGoals(id string) int { return f.counts[id] }
func (f *fakeGoalCounter) BumpRealGoals(id string) {
	if f.counts == nil {
		f.counts = make(map[string]int)
	}
	f.counts[id]++
}

func TestGoalEmitter_PublishesGoalScored(t *testing.T) {
	roster := NewRosterTracker(NewBus())
	roster.Observe(Event{Name: "UpdateState", Raw: makeUpdateStateRoster(t, []rosterPlayerStub{{ID: "Steam|1|0", Name: "Ada", Team: 0}})})
	correlation := NewCorrelationBuffer(8)
	e := NewGoalEmitter(roster, correlation, NewTickStore(), &fakeFlipReset{}, &fakeGoalCounter{})

	out := e.Process(makeGoalScored(t, "Ada", 100))
	if len(out) != 1 || out[0].Name != "_GoalScored" {
		t.Fatalf("expected _GoalScored, got %v", out)
	}
	var payload struct {
		Scorer    *EnrichedPlayer `json:"scorer"`
		GoalSpeed *float64        `json:"goalSpeed"`
	}
	if err := json.Unmarshal(out[0].Data, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload.Scorer == nil || payload.Scorer.ID != "Steam|1|0" {
		t.Fatalf("scorer not resolved: %+v", payload.Scorer)
	}
	if payload.GoalSpeed == nil || *payload.GoalSpeed != 100 {
		t.Fatalf("goalSpeed dropped: %+v", payload.GoalSpeed)
	}
}

func TestGoalEmitter_BumpsRealGoalsForCleanGoal(t *testing.T) {
	roster := NewRosterTracker(NewBus())
	roster.Observe(Event{Name: "UpdateState", Raw: makeUpdateStateRoster(t, []rosterPlayerStub{{ID: "Steam|1|0", Name: "Ada", Team: 0}})})
	gc := &fakeGoalCounter{}
	e := NewGoalEmitter(roster, NewCorrelationBuffer(8), NewTickStore(), &fakeFlipReset{}, gc)

	_ = e.Process(makeGoalScored(t, "Ada", 100))
	if gc.RealGoals("Steam|1|0") != 1 {
		t.Fatalf("expected real-goal bump, got %d", gc.RealGoals("Steam|1|0"))
	}
}

func TestGoalEmitter_OwnGoalSkipsRealGoalBump(t *testing.T) {
	roster := NewRosterTracker(NewBus())
	roster.Observe(Event{Name: "UpdateState", Raw: makeUpdateStateRoster(t, []rosterPlayerStub{
		{ID: "Steam|1|0", Name: "Ada", Team: 0},
		{ID: "Steam|2|0", Name: "Ben", Team: 1},
	})})
	correlation := NewCorrelationBuffer(8)
	// Last toucher is on the conceding team — own-goal heuristic
	// should fire without bumping the real-goal counter.
	correlation.Record("BallHit", &ballHitRecord{
		Player: &EnrichedPlayer{ID: "Steam|2|0", Team: 1},
	})
	gc := &fakeGoalCounter{}
	e := NewGoalEmitter(roster, correlation, NewTickStore(), &fakeFlipReset{}, gc)

	_ = e.Process(makeGoalScoredFor(t, "Ada", 0, 100))
	if gc.RealGoals("Steam|1|0") != 0 {
		t.Fatalf("own goal should not bump real-goal counter, got %d", gc.RealGoals("Steam|1|0"))
	}
}

func TestGoalEmitter_SoloOwnGoal(t *testing.T) {
	// Solo / no-opponents private match: RL credits the deflector
	// themselves as Scorer. lastToucher.Team == scorer.Team. The
	// emitter should still flag it and flip the scoring/conceding
	// teams to match the actual score change.
	roster := NewRosterTracker(NewBus())
	roster.Observe(Event{Name: "UpdateState", Raw: makeUpdateStateRoster(t, []rosterPlayerStub{
		{ID: "Steam|1|0", Name: "Ada", Team: 0},
	})})
	correlation := NewCorrelationBuffer(8)
	correlation.Record("BallHit", &ballHitRecord{
		Player: &EnrichedPlayer{ID: "Steam|1|0", Team: 0},
	})
	gc := &fakeGoalCounter{}
	e := NewGoalEmitter(roster, correlation, NewTickStore(), &fakeFlipReset{}, gc)

	out := e.Process(makeGoalScoredFor(t, "Ada", 0, 100))
	if len(out) != 1 {
		t.Fatalf("expected one event, got %v", out)
	}
	var payload struct {
		IsOwnGoal     bool `json:"isOwnGoal"`
		ScoringTeam   *int `json:"scoringTeam,omitempty"`
		ConcedingTeam *int `json:"concedingTeam,omitempty"`
	}
	if err := json.Unmarshal(out[0].Data, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !payload.IsOwnGoal {
		t.Fatalf("solo own goal should flag isOwnGoal=true")
	}
	if payload.ScoringTeam == nil || *payload.ScoringTeam != 1 {
		t.Errorf("scoringTeam should flip to 1 (the team that gained the score), got %v", payload.ScoringTeam)
	}
	if payload.ConcedingTeam == nil || *payload.ConcedingTeam != 0 {
		t.Errorf("concedingTeam should be 0 (the deflector's team), got %v", payload.ConcedingTeam)
	}
	if gc.RealGoals("Steam|1|0") != 0 {
		t.Errorf("solo own goal should not bump real-goal counter, got %d", gc.RealGoals("Steam|1|0"))
	}
}

func TestGoalEmitter_GoalReplayStartedOnReplayEdge(t *testing.T) {
	roster := NewRosterTracker(NewBus())
	roster.Observe(Event{Name: "UpdateState", Raw: makeUpdateStateRoster(t, []rosterPlayerStub{{ID: "Steam|1|0", Name: "Ada", Team: 0}})})
	ticks := NewTickStore()
	e := NewGoalEmitter(roster, NewCorrelationBuffer(8), ticks, &fakeFlipReset{}, &fakeGoalCounter{})

	// Cache a goal first.
	if got := e.Process(makeGoalScored(t, "Ada", 100)); len(got) != 1 {
		t.Fatalf("setup: expected _GoalScored, got %v", got)
	}

	// Tick with bReplay=false then bReplay=true → rising edge.
	ticks.Observe(updateStateTick(t, "G1", 0, 0, false))
	if got := e.Process(updateStateTick(t, "G1", 0, 0, false)); len(got) != 0 {
		t.Fatalf("no replay edge yet, got %v", got)
	}
	ticks.Observe(updateStateTick(t, "G1", 0, 0, true))
	out := e.Process(updateStateTick(t, "G1", 0, 0, true))
	if len(out) != 1 || out[0].Name != "_GoalReplayStarted" {
		t.Fatalf("expected _GoalReplayStarted, got %v", out)
	}
}

func TestGoalEmitter_ResetsCacheOnMatchBoundary(t *testing.T) {
	roster := NewRosterTracker(NewBus())
	roster.Observe(Event{Name: "UpdateState", Raw: makeUpdateStateRoster(t, []rosterPlayerStub{{ID: "Steam|1|0", Name: "Ada", Team: 0}})})
	ticks := NewTickStore()
	e := NewGoalEmitter(roster, NewCorrelationBuffer(8), ticks, &fakeFlipReset{}, &fakeGoalCounter{})

	_ = e.Process(makeGoalScored(t, "Ada", 100))
	_ = e.Process(Event{Name: "MatchCreated"})

	ticks.Observe(updateStateTick(t, "G1", 0, 0, false))
	ticks.Observe(updateStateTick(t, "G1", 0, 0, true))
	out := e.Process(updateStateTick(t, "G1", 0, 0, true))
	if len(out) != 0 {
		t.Fatalf("MatchCreated should clear cached goal — no _GoalReplayStarted expected, got %v", out)
	}
}

func makeGoalScored(t *testing.T, scorerName string, speed float64) Event {
	t.Helper()
	return makeGoalScoredFor(t, scorerName, 0, speed)
}

func makeGoalScoredFor(t *testing.T, scorerName string, scorerTeam int, speed float64) Event {
	t.Helper()
	inner, _ := json.Marshal(map[string]any{
		"MatchGuid": "G1",
		"Scorer":    map[string]any{"Name": scorerName, "Shortcut": 0, "TeamNum": scorerTeam},
		"GoalSpeed": speed,
	})
	raw, _ := json.Marshal(map[string]any{"Event": "GoalScored", "Data": string(inner)})
	return Event{Name: "GoalScored", Raw: raw}
}

func TestGoalEmitter_ModifiersAlwaysPresent(t *testing.T) {
	roster := NewRosterTracker(NewBus())
	roster.Observe(Event{Name: "UpdateState", Raw: makeUpdateStateRoster(t, []rosterPlayerStub{{ID: "Steam|1|0", Name: "Ada", Team: 0}})})
	correlation := NewCorrelationBuffer(8)
	correlation.Record("StatfeedEvent", &statfeedRecord{
		EventName: "AerialGoal",
		MainRef:   &ShortcutRef{Name: "Ada"},
	})
	e := NewGoalEmitter(roster, correlation, NewTickStore(), &fakeFlipReset{}, &fakeGoalCounter{})

	out := e.Process(makeGoalScored(t, "Ada", 100))
	if len(out) != 1 {
		t.Fatalf("expected one event, got %v", out)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(out[0].Data, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	modsRaw, ok := payload["modifiers"]
	if !ok {
		t.Fatalf("modifiers field must always be present")
	}
	var mods map[string]bool
	if err := json.Unmarshal(modsRaw, &mods); err != nil {
		t.Fatalf("unmarshal modifiers: %v", err)
	}
	if mods["isAerialGoal"] != true {
		t.Errorf("isAerialGoal: want true, got %v", mods["isAerialGoal"])
	}
	for _, k := range []string{"isHatTrickGoal", "isBackwardsGoal", "isBicycleGoal", "isFlipResetGoal"} {
		if _, has := mods[k]; !has {
			t.Errorf("%s: must be present (false), got missing", k)
		}
		if mods[k] != false {
			t.Errorf("%s: want false, got %v", k, mods[k])
		}
	}
}

func TestGoalEmitter_ConsumesModifierStatfeeds(t *testing.T) {
	roster := NewRosterTracker(NewBus())
	roster.Observe(Event{Name: "UpdateState", Raw: makeUpdateStateRoster(t, []rosterPlayerStub{{ID: "Steam|1|0", Name: "Ada", Team: 0}})})
	correlation := NewCorrelationBuffer(8)
	correlation.Record("StatfeedEvent", &statfeedRecord{
		EventName: "AerialGoal",
		MainRef:   &ShortcutRef{Name: "Ada"},
	})
	e := NewGoalEmitter(roster, correlation, NewTickStore(), &fakeFlipReset{}, &fakeGoalCounter{})

	// First goal: aerial.
	out := e.Process(makeGoalScored(t, "Ada", 100))
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(out[0].Data, &payload); err != nil {
		t.Fatal(err)
	}
	var mods map[string]bool
	_ = json.Unmarshal(payload["modifiers"], &mods)
	if !mods["isAerialGoal"] {
		t.Fatalf("first goal: expected isAerialGoal=true, got %+v", mods)
	}

	// Second goal: same buffer, no new statfeed — should be plain.
	out = e.Process(makeGoalScored(t, "Ada", 100))
	if err := json.Unmarshal(out[0].Data, &payload); err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(payload["modifiers"], &mods)
	if mods["isAerialGoal"] {
		t.Fatalf("second goal must not inherit modifier from first; got %+v", mods)
	}
}
