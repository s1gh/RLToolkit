package main

import (
	"encoding/json"
	"testing"
)

func TestFirstBloodEmitter_FirstTouchOnce(t *testing.T) {
	e := NewFirstBloodEmitter()

	_ = e.Process(Event{Name: "MatchInitialized"})
	_ = e.Process(Event{Name: "RoundStarted"})

	out1 := e.Process(syntheticBallHit(t))
	if !hasName(out1, "_FirstTouch") {
		t.Fatalf("expected _FirstTouch on first BallHit, got %v", names(out1))
	}

	out2 := e.Process(syntheticBallHit(t))
	if hasName(out2, "_FirstTouch") {
		t.Fatalf("_FirstTouch fired twice")
	}
}

func TestFirstBloodEmitter_FirstTouchRearmsOnRoundStarted(t *testing.T) {
	e := NewFirstBloodEmitter()
	_ = e.Process(Event{Name: "MatchInitialized"})
	_ = e.Process(Event{Name: "RoundStarted"})
	_ = e.Process(syntheticBallHit(t))
	// Second round.
	_ = e.Process(Event{Name: "RoundStarted"})
	out := e.Process(syntheticBallHit(t))
	if !hasName(out, "_FirstTouch") {
		t.Fatalf("expected _FirstTouch after second RoundStarted")
	}
}

func TestFirstBloodEmitter_FirstBloodOnce(t *testing.T) {
	e := NewFirstBloodEmitter()
	_ = e.Process(Event{Name: "MatchInitialized"})
	_ = e.Process(Event{Name: "RoundStarted"})

	goal := syntheticGoal(t, 80)
	out1 := e.Process(goal)
	if !hasName(out1, "_FirstBlood") {
		t.Fatalf("expected _FirstBlood on first goal, got %v", names(out1))
	}

	out2 := e.Process(goal)
	if hasName(out2, "_FirstBlood") {
		t.Fatalf("_FirstBlood fired twice")
	}
}

func TestFirstBloodEmitter_OvertimeOnce(t *testing.T) {
	e := NewFirstBloodEmitter()
	_ = e.Process(Event{Name: "MatchInitialized"})

	out1 := e.Process(updateStateWithOvertime(t, true))
	if !hasName(out1, "_OvertimeStarted") {
		t.Fatalf("expected _OvertimeStarted on rising bOvertime edge, got %v", names(out1))
	}

	out2 := e.Process(updateStateWithOvertime(t, true))
	if hasName(out2, "_OvertimeStarted") {
		t.Fatalf("_OvertimeStarted fired twice")
	}
}

func TestFirstBloodEmitter_OvertimeIgnoredBeforeRisingEdge(t *testing.T) {
	e := NewFirstBloodEmitter()
	_ = e.Process(Event{Name: "MatchInitialized"})
	out := e.Process(updateStateWithOvertime(t, false))
	if hasName(out, "_OvertimeStarted") {
		t.Fatalf("_OvertimeStarted fired without a rising edge")
	}
}

func TestFirstBloodEmitter_ResetsOnMatchBoundary(t *testing.T) {
	e := NewFirstBloodEmitter()
	_ = e.Process(Event{Name: "MatchInitialized"})
	_ = e.Process(Event{Name: "RoundStarted"})
	_ = e.Process(syntheticGoal(t, 80))
	_ = e.Process(updateStateWithOvertime(t, true))

	_ = e.Process(Event{Name: "MatchCreated"})
	_ = e.Process(Event{Name: "MatchInitialized"})
	_ = e.Process(Event{Name: "RoundStarted"})

	if !hasName(e.Process(syntheticGoal(t, 80)), "_FirstBlood") {
		t.Fatal("_FirstBlood should re-arm after MatchCreated")
	}
	// OT also re-armed: needs a fresh rising edge — feed false, then true.
	_ = e.Process(updateStateWithOvertime(t, false))
	if !hasName(e.Process(updateStateWithOvertime(t, true)), "_OvertimeStarted") {
		t.Fatal("_OvertimeStarted should re-arm after MatchCreated")
	}
}

func syntheticBallHit(t *testing.T) Event {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"matchGuid": "G",
		"players":   []*EnrichedPlayer{{ID: "Steam|1|0", Name: "A", Team: 0}},
	})
	return Event{Name: "_BallHit", Data: body}
}

func updateStateWithOvertime(t *testing.T, overtime bool) Event {
	t.Helper()
	inner, _ := json.Marshal(map[string]any{
		"MatchGuid": "G",
		"Game":      map[string]any{"bOvertime": overtime},
		"Teams": []map[string]any{
			{"TeamNum": 0, "Score": 3},
			{"TeamNum": 1, "Score": 3},
		},
	})
	raw, _ := json.Marshal(map[string]any{"Event": "UpdateState", "Data": string(inner)})
	return Event{Name: "UpdateState", Raw: raw}
}

func hasName(evts []Event, name string) bool {
	for _, e := range evts {
		if e.Name == name {
			return true
		}
	}
	return false
}
