package backend

import (
	"encoding/json"
	"rl-toolkit/internal/bus"
	"testing"
)

func TestDemosEmitter_PublishesPlayerDemolished(t *testing.T) {
	e := NewDemosEmitter(NewTickStore())
	out := e.Process(makeDemolishEvt(t, "Steam|1|0", 0, "Steam|2|0", 1))
	if !hasName(out, "_PlayerDemolished") {
		t.Fatalf("expected _PlayerDemolished, got %v", names(out))
	}
	if hasName(out, "_DemoChain") {
		t.Fatalf("first demo should not yet fire _DemoChain, got %v", names(out))
	}
}

func TestDemosEmitter_DemoChainAfterTwoDemos(t *testing.T) {
	e := NewDemosEmitter(NewTickStore())
	_ = e.Process(makeDemolishEvt(t, "Steam|1|0", 0, "Steam|2|0", 1))
	out := e.Process(makeDemolishEvt(t, "Steam|1|0", 0, "Steam|3|0", 1))
	if !hasName(out, "_DemoChain") {
		t.Fatalf("second demo should fire _DemoChain, got %v", names(out))
	}
}

func TestDemosEmitter_SkipsSelfAndTeamDemos(t *testing.T) {
	e := NewDemosEmitter(NewTickStore())
	// Same-team — should not extend chain history.
	_ = e.Process(makeDemolishEvt(t, "Steam|1|0", 0, "Steam|9|0", 0))
	_ = e.Process(makeDemolishEvt(t, "Steam|1|0", 0, "Steam|9|0", 0))
	out := e.Process(makeDemolishEvt(t, "Steam|1|0", 0, "Steam|2|0", 1))
	if hasName(out, "_DemoChain") {
		t.Fatalf("team-demos shouldn't seed the chain, got %v", names(out))
	}
}

func TestDemosEmitter_DemolishLogClearsOnMatchBoundary(t *testing.T) {
	e := NewDemosEmitter(NewTickStore())
	_ = e.Process(makeDemolishEvt(t, "Steam|1|0", 0, "Steam|2|0", 1))
	if len(e.DemolishLog()) != 1 {
		t.Fatalf("expected 1 demolish log entry, got %d", len(e.DemolishLog()))
	}
	_ = e.Process(bus.Event{Name: "MatchCreated"})
	if got := e.DemolishLog(); got != nil {
		t.Fatalf("DemolishLog should clear on MatchCreated, got %v", got)
	}
}

func TestDemosEmitter_IgnoresOtherEvents(t *testing.T) {
	e := NewDemosEmitter(NewTickStore())
	if got := e.Process(bus.Event{Name: "UpdateState"}); len(got) != 0 {
		t.Fatalf("non-_Demolish events should be ignored, got %v", got)
	}
}

func makeDemolishEvt(t *testing.T, attackerID string, attackerTeam int, victimID string, victimTeam int) bus.Event {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"matchGuid": "G1",
		"attacker":  &EnrichedPlayer{ID: attackerID, Name: "Att", Team: attackerTeam},
		"victim":    &EnrichedPlayer{ID: victimID, Name: "Vic", Team: victimTeam},
	})
	return bus.Event{Name: "_Demolish", Data: body}
}
