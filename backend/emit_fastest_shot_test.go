package backend

import (
	"encoding/json"
	"rl-toolkit/internal/bus"
	"testing"
)

func TestFastestShotEmitter_FiresOnNewMaximum(t *testing.T) {
	e := NewFastestShotEmitter()

	out1 := e.Process(syntheticGoal(t, 80))
	if len(out1) != 1 || out1[0].Name != "_FastestShotOfMatch" {
		t.Fatalf("first goal: expected 1 _FastestShotOfMatch, got %v", out1)
	}

	out2 := e.Process(syntheticGoal(t, 70))
	if len(out2) != 0 {
		t.Fatalf("slower goal: expected no emission, got %v", out2)
	}

	out3 := e.Process(syntheticGoal(t, 100))
	if len(out3) != 1 {
		t.Fatalf("faster goal: expected 1 emission, got %v", out3)
	}
}

func TestFastestShotEmitter_ResetsOnMatchBoundary(t *testing.T) {
	e := NewFastestShotEmitter()
	_ = e.Process(syntheticGoal(t, 100))
	_ = e.Process(bus.Event{Name: "MatchCreated"})
	out := e.Process(syntheticGoal(t, 50))
	if len(out) != 1 {
		t.Fatalf("expected emission after reset, got %v", out)
	}
}

func TestFastestShotEmitter_BallHitTracked(t *testing.T) {
	e := NewFastestShotEmitter()
	speed := 65.0
	body, _ := json.Marshal(map[string]any{
		"postHitSpeed": speed,
		"players":      []map[string]any{{"id": "1", "name": "Tester", "team": 0}},
	})
	out := e.Process(bus.Event{Name: "_BallHit", Data: body})
	if len(out) != 1 {
		t.Fatalf("ball hit: expected 1 emission, got %v", out)
	}
	var payload struct {
		Source string  `json:"source"`
		Speed  float64 `json:"speed"`
	}
	if err := json.Unmarshal(out[0].Data, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload.Source != "BallHit" || payload.Speed != 65.0 {
		t.Fatalf("payload mismatch: %+v", payload)
	}
}

// TestFastestShotEmitter_DecodesFromRaw guards the bridge that lets
// the emitter consume legacy-shaped events: when the upstream producer
// (Stage 5 Synthesizer) marshals a flat JSON envelope into evt.Raw and
// leaves evt.Data empty, the emitter must still extract the speed
// field. Regressing this would silently zero out _FastestShotOfMatch
// in production until emit_ball_hit / emit_goal land.
func TestFastestShotEmitter_DecodesFromRaw(t *testing.T) {
	e := NewFastestShotEmitter()
	speed := 90.0
	raw, _ := json.Marshal(map[string]any{
		"Event":     "_GoalScored",
		"goalSpeed": speed,
		"scorer":    map[string]any{"id": "9", "name": "Ada", "team": 1},
	})
	out := e.Process(bus.Event{Name: "_GoalScored", Raw: raw})
	if len(out) != 1 {
		t.Fatalf("expected 1 emission from Raw-only event, got %v", out)
	}
}

func syntheticGoal(t *testing.T, speed float64) bus.Event {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"goalSpeed": speed,
		"scorer":    map[string]any{"id": "1", "name": "Tester", "team": 0},
	})
	return bus.Event{Name: "_GoalScored", Data: body}
}
