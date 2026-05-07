package emit

import (
	"encoding/json"
	"rl-toolkit/backend/internal/bus"
	"rl-toolkit/backend/internal/types"
	"testing"
)

// nullTicks is a TickReader that always returns "no scalar known" —
// matches the empty-TickStore behavior the legacy tests relied on.
type nullTicks struct{}

func (nullTicks) PlayerScalars(string) (*float64, bool) { return nil, false }

func TestDemos_PublishesPlayerDemolished(t *testing.T) {
	e := NewDemos(nullTicks{})
	out := e.Process(makeDemolishEvt(t, "Steam|1|0", 0, "Steam|2|0", 1))
	if !hasName(out, "_PlayerDemolished") {
		t.Fatalf("expected _PlayerDemolished, got %v", evtNames(out))
	}
	if hasName(out, "_DemoChain") {
		t.Fatalf("first demo should not yet fire _DemoChain, got %v", evtNames(out))
	}
}

func TestDemos_DemoChainAfterTwoDemos(t *testing.T) {
	e := NewDemos(nullTicks{})
	_ = e.Process(makeDemolishEvt(t, "Steam|1|0", 0, "Steam|2|0", 1))
	out := e.Process(makeDemolishEvt(t, "Steam|1|0", 0, "Steam|3|0", 1))
	if !hasName(out, "_DemoChain") {
		t.Fatalf("second demo should fire _DemoChain, got %v", evtNames(out))
	}
}

func TestDemos_SkipsSelfAndTeamDemos(t *testing.T) {
	e := NewDemos(nullTicks{})
	// Same-team — should not extend chain history.
	_ = e.Process(makeDemolishEvt(t, "Steam|1|0", 0, "Steam|9|0", 0))
	_ = e.Process(makeDemolishEvt(t, "Steam|1|0", 0, "Steam|9|0", 0))
	out := e.Process(makeDemolishEvt(t, "Steam|1|0", 0, "Steam|2|0", 1))
	if hasName(out, "_DemoChain") {
		t.Fatalf("team-demos shouldn't seed the chain, got %v", evtNames(out))
	}
}

func TestDemos_DemolishLogClearsOnMatchBoundary(t *testing.T) {
	e := NewDemos(nullTicks{})
	_ = e.Process(makeDemolishEvt(t, "Steam|1|0", 0, "Steam|2|0", 1))
	if len(e.DemolishLog()) != 1 {
		t.Fatalf("expected 1 demolish log entry, got %d", len(e.DemolishLog()))
	}
	_ = e.Process(bus.Event{Name: "MatchCreated"})
	if got := e.DemolishLog(); got != nil {
		t.Fatalf("DemolishLog should clear on MatchCreated, got %v", got)
	}
}

func TestDemos_IgnoresOtherEvents(t *testing.T) {
	e := NewDemos(nullTicks{})
	if got := e.Process(bus.Event{Name: "UpdateState"}); len(got) != 0 {
		t.Fatalf("non-_Demolish events should be ignored, got %v", got)
	}
}

// TestDemos_AttackerScalarsFromTickReader confirms speed/supersonic
// flow through from the injected TickReader into the payload.
func TestDemos_AttackerScalarsFromTickReader(t *testing.T) {
	speed := 2200.0
	ticks := stubTicks{speed: &speed, supersonic: true}
	e := NewDemos(ticks)
	out := e.Process(makeDemolishEvt(t, "Steam|1|0", 0, "Steam|2|0", 1))
	if len(out) == 0 {
		t.Fatal("no events emitted")
	}
	var payload struct {
		AttackerSpeed         *float64 `json:"attackerSpeed"`
		AttackerWasSupersonic *bool    `json:"attackerWasSupersonic"`
	}
	if err := json.Unmarshal(out[0].Data, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.AttackerSpeed == nil || *payload.AttackerSpeed != speed {
		t.Errorf("speed: got %v, want %v", payload.AttackerSpeed, speed)
	}
	if payload.AttackerWasSupersonic == nil || !*payload.AttackerWasSupersonic {
		t.Errorf("supersonic: got %v, want true", payload.AttackerWasSupersonic)
	}
}

type stubTicks struct {
	speed      *float64
	supersonic bool
}

func (s stubTicks) PlayerScalars(string) (*float64, bool) { return s.speed, s.supersonic }

func makeDemolishEvt(t *testing.T, attackerID string, attackerTeam int, victimID string, victimTeam int) bus.Event {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"matchGuid": "G1",
		"attacker":  &types.EnrichedPlayer{ID: attackerID, Name: "Att", Team: attackerTeam},
		"victim":    &types.EnrichedPlayer{ID: victimID, Name: "Vic", Team: victimTeam},
	})
	return bus.Event{Name: "_Demolish", Data: body}
}
