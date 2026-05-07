package backend

import (
	"encoding/json"
	"testing"
	"time"
)

func TestCrossbarEmitter_FiresOnFirstHit(t *testing.T) {
	e := NewCrossbarEmitter(NewRosterTracker(NewBus()), nil)
	out := e.Process(makeCrossbarHit(t))
	if len(out) != 1 || out[0].Name != "_CrossbarHit" {
		t.Fatalf("expected _CrossbarHit, got %v", out)
	}
}

func TestCrossbarEmitter_DebouncesBurst(t *testing.T) {
	e := NewCrossbarEmitter(NewRosterTracker(NewBus()), nil)
	_ = e.Process(makeCrossbarHit(t))
	out := e.Process(makeCrossbarHit(t))
	if len(out) != 0 {
		t.Fatalf("burst hit should be debounced, got %v", out)
	}
}

func TestCrossbarEmitter_FiresAgainAfterWindow(t *testing.T) {
	e := NewCrossbarEmitter(NewRosterTracker(NewBus()), nil)
	_ = e.Process(makeCrossbarHit(t))
	// Rewind the lastHit timestamp past the debounce window so the
	// next hit looks fresh without sleeping.
	e.lastHit = time.Now().Add(-crossbarDebounceWindow - time.Second)
	out := e.Process(makeCrossbarHit(t))
	if len(out) != 1 {
		t.Fatalf("hit after window should fire, got %v", out)
	}
}

func TestCrossbarEmitter_SkippedDuringReplay(t *testing.T) {
	ticks := NewTickStore()
	ticks.Observe(updateStateTick(t, "G1", 0, 0, true))
	e := NewCrossbarEmitter(NewRosterTracker(NewBus()), ticks)
	out := e.Process(makeCrossbarHit(t))
	if len(out) != 0 {
		t.Fatalf("crossbar during replay should be skipped, got %v", out)
	}
}

func TestCrossbarEmitter_IgnoresOtherEvents(t *testing.T) {
	e := NewCrossbarEmitter(NewRosterTracker(NewBus()), nil)
	if got := e.Process(Event{Name: "Other"}); len(got) != 0 {
		t.Fatalf("non-CrossbarHit should be ignored, got %v", got)
	}
}

func makeCrossbarHit(t *testing.T) Event {
	t.Helper()
	inner, _ := json.Marshal(map[string]any{
		"MatchGuid":    "G1",
		"BallSpeed":    120.0,
		"ImpactForce":  500.0,
		"BallLocation": map[string]any{"x": 0, "y": 5000, "z": 600},
		"BallLastTouch": map[string]any{
			"Player": map[string]any{"Name": "Ada", "TeamNum": 0, "Shortcut": 0},
			"Speed":  60.0,
		},
	})
	raw, _ := json.Marshal(map[string]any{"Event": "CrossbarHit", "Data": string(inner)})
	return Event{Name: "CrossbarHit", Raw: raw}
}
