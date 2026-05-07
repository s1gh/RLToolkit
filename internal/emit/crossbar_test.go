package emit

import (
	"encoding/json"
	"rl-toolkit/internal/bus"
	"rl-toolkit/internal/types"
	"testing"
	"time"
)

// nullRoster is a RosterResolver that always returns a stub built
// from the ref alone. Matches the behavior of an empty RosterTracker.
type nullRoster struct{}

func (nullRoster) ResolveByShortcut(ref types.ShortcutRef) *types.EnrichedPlayer {
	if ref.Name == "" {
		return nil
	}
	return &types.EnrichedPlayer{Name: ref.Name, Team: ref.TeamNum}
}

// stubGate is a fixed-state ReplayGate.
type stubGate struct{ inReplay bool }

func (g stubGate) InReplay() bool { return g.inReplay }

func TestCrossbar_FiresOnFirstHit(t *testing.T) {
	e := NewCrossbar(nullRoster{}, nil)
	out := e.Process(makeCrossbarHit(t))
	if len(out) != 1 || out[0].Name != "_CrossbarHit" {
		t.Fatalf("expected _CrossbarHit, got %v", out)
	}
}

func TestCrossbar_DebouncesBurst(t *testing.T) {
	e := NewCrossbar(nullRoster{}, nil)
	_ = e.Process(makeCrossbarHit(t))
	out := e.Process(makeCrossbarHit(t))
	if len(out) != 0 {
		t.Fatalf("burst hit should be debounced, got %v", out)
	}
}

func TestCrossbar_FiresAgainAfterWindow(t *testing.T) {
	e := NewCrossbar(nullRoster{}, nil)
	_ = e.Process(makeCrossbarHit(t))
	// Rewind the lastHit timestamp past the debounce window so the
	// next hit looks fresh without sleeping.
	e.lastHit = time.Now().Add(-crossbarDebounceWindow - time.Second)
	out := e.Process(makeCrossbarHit(t))
	if len(out) != 1 {
		t.Fatalf("hit after window should fire, got %v", out)
	}
}

func TestCrossbar_SkippedDuringReplay(t *testing.T) {
	e := NewCrossbar(nullRoster{}, stubGate{inReplay: true})
	out := e.Process(makeCrossbarHit(t))
	if len(out) != 0 {
		t.Fatalf("crossbar during replay should be skipped, got %v", out)
	}
}

func TestCrossbar_IgnoresOtherEvents(t *testing.T) {
	e := NewCrossbar(nullRoster{}, nil)
	if got := e.Process(bus.Event{Name: "Other"}); len(got) != 0 {
		t.Fatalf("non-CrossbarHit should be ignored, got %v", got)
	}
}

func makeCrossbarHit(t *testing.T) bus.Event {
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
	return bus.Event{Name: "CrossbarHit", Raw: raw}
}
