package backend

import (
	"encoding/json"
	"testing"
)

// fakeRealGoals is a minimal realGoalsLookup used by tests that don't
// need a full Synthesizer for HatTrick suppression coverage.
type fakeRealGoals struct{ counts map[string]int }

func (f *fakeRealGoals) RealGoals(id string) int { return f.counts[id] }

func TestStatfeedEmitter_PublishesCatchall(t *testing.T) {
	roster := NewRosterTracker(NewBus())
	correlation := NewCorrelationBuffer(8)
	e := NewStatfeedEmitter(roster, correlation, nil, &fakeRealGoals{})

	out := e.Process(makeStatfeed(t, "Save", "Ada", 1, "", 0))
	if !hasName(out, "_StatfeedEvent") {
		t.Fatalf("expected _StatfeedEvent in %v", names(out))
	}
}

func TestStatfeedEmitter_PromotesKnownVariants(t *testing.T) {
	roster := NewRosterTracker(NewBus())
	correlation := NewCorrelationBuffer(8)
	e := NewStatfeedEmitter(roster, correlation, nil, &fakeRealGoals{})

	cases := []struct {
		name    string
		variant string
	}{
		{"_Save", "Save"},
		{"_EpicSave", "EpicSave"},
		{"_Shot", "Shot"},
		{"_Assist", "Assist"},
		{"_Center", "Center"},
		{"_Clear", "Clear"},
		{"_BicycleHit", "BicycleHit"},
		{"_FlipReset", "FlipReset"},
	}
	for _, c := range cases {
		out := e.Process(makeStatfeed(t, c.variant, "Ada", 0, "", 0))
		if !hasName(out, c.name) {
			t.Fatalf("variant %q: expected %q in %v", c.variant, c.name, names(out))
		}
	}
}

func TestStatfeedEmitter_FlipResetArmsAndConsumes(t *testing.T) {
	roster := NewRosterTracker(NewBus())
	// Seed the roster so ResolveByShortcut returns a real ID.
	roster.Observe(Event{Name: "UpdateState", Raw: makeUpdateStateRoster(t, []rosterPlayerStub{{ID: "Steam|7|0", Name: "Ada", Team: 0}})})
	e := NewStatfeedEmitter(roster, NewCorrelationBuffer(8), nil, &fakeRealGoals{})

	_ = e.Process(makeStatfeed(t, "FlipReset", "Ada", 0, "", 0))
	if !e.ConsumeFlipResetArm("Steam|7|0") {
		t.Fatal("expected flip-reset to be armed for the player after FlipReset statfeed")
	}
	if e.ConsumeFlipResetArm("Steam|7|0") {
		t.Fatal("expected flip-reset to be cleared after consume")
	}
}

func TestStatfeedEmitter_HatTrickSuppressedBelowThreshold(t *testing.T) {
	roster := NewRosterTracker(NewBus())
	roster.Observe(Event{Name: "UpdateState", Raw: makeUpdateStateRoster(t, []rosterPlayerStub{{ID: "Steam|7|0", Name: "Ada", Team: 0}})})
	rg := &fakeRealGoals{counts: map[string]int{"Steam|7|0": 2}}
	e := NewStatfeedEmitter(roster, NewCorrelationBuffer(8), nil, rg)

	out := e.Process(makeStatfeed(t, "HatTrick", "Ada", 0, "", 0))
	if hasName(out, "_HatTrick") {
		t.Fatalf("HatTrick should be suppressed when real goals < 3, got %v", names(out))
	}

	rg.counts["Steam|7|0"] = 3
	out = e.Process(makeStatfeed(t, "HatTrick", "Ada", 0, "", 0))
	if !hasName(out, "_HatTrick") {
		t.Fatalf("HatTrick should fire at 3 real goals, got %v", names(out))
	}
}

func TestStatfeedEmitter_DemolishPublishesTypedEvent(t *testing.T) {
	roster := NewRosterTracker(NewBus())
	roster.Observe(Event{Name: "UpdateState", Raw: makeUpdateStateRoster(t, []rosterPlayerStub{
		{ID: "Steam|1|0", Name: "Ada", Team: 0},
		{ID: "Steam|2|0", Name: "Ben", Team: 1},
	})})
	e := NewStatfeedEmitter(roster, NewCorrelationBuffer(8), nil, &fakeRealGoals{})

	out := e.Process(makeStatfeed(t, "Demolish", "Ada", 0, "Ben", 1))
	if !hasName(out, "_Demolish") {
		t.Fatalf("expected _Demolish in %v", names(out))
	}
	// _PlayerDemolished and _DemoChain are DemosEmitter's job now.
	if hasName(out, "_PlayerDemolished") || hasName(out, "_DemoChain") {
		t.Fatalf("StatfeedEmitter should no longer emit demo events directly: %v", names(out))
	}
}

func TestStatfeedEmitter_UnknownStatfeedFires(t *testing.T) {
	roster := NewRosterTracker(NewBus())
	roster.Observe(Event{Name: "UpdateState", Raw: makeUpdateStateRoster(t, []rosterPlayerStub{{ID: "Steam|7|0", Name: "Ada", Team: 0}})})
	e := NewStatfeedEmitter(roster, NewCorrelationBuffer(8), nil, &fakeRealGoals{})

	out := e.Process(makeStatfeed(t, "MysteryStatfeed", "Ada", 0, "", 0))
	if !hasName(out, "_UnknownStatfeed") {
		t.Fatalf("expected _UnknownStatfeed for unrecognized variant, got %v", names(out))
	}
}

func makeStatfeed(t *testing.T, variant, mainName string, mainTeam int, secondaryName string, secondaryTeam int) Event {
	t.Helper()
	inner := map[string]any{
		"MatchGuid":  "G1",
		"EventName":  variant,
		"MainTarget": map[string]any{"Name": mainName, "Shortcut": 0, "TeamNum": mainTeam},
	}
	if secondaryName != "" {
		inner["SecondaryTarget"] = map[string]any{"Name": secondaryName, "Shortcut": 0, "TeamNum": secondaryTeam}
	}
	innerBytes, _ := json.Marshal(inner)
	raw, _ := json.Marshal(map[string]any{"Event": "StatfeedEvent", "Data": string(innerBytes)})
	return Event{Name: "StatfeedEvent", Raw: raw}
}
