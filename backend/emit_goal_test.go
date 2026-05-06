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
