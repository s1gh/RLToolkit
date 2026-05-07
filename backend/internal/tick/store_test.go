package tick

import (
	"encoding/json"
	"rl-toolkit/backend/internal/bus"
	"testing"
)

func TestStore_AdvancesOnEachUpdateState(t *testing.T) {
	ts := New()
	if ts.Latest() != nil || ts.Previous() != nil {
		t.Fatal("expected empty store on construction")
	}

	ts.Observe(updateStateTick(t, "G1", 0, 0, false))
	if ts.Latest() == nil {
		t.Fatal("Latest nil after first tick")
	}
	if ts.Previous() != nil {
		t.Fatal("Previous should be nil after first tick")
	}

	ts.Observe(updateStateTick(t, "G1", 1, 0, false))
	if ts.Latest() == nil || ts.Previous() == nil {
		t.Fatal("expected both Latest and Previous after second tick")
	}
	if ts.Latest().Teams[0].Score != 1 {
		t.Fatalf("Latest score = %d, want 1", ts.Latest().Teams[0].Score)
	}
	if ts.Previous().Teams[0].Score != 0 {
		t.Fatalf("Previous score = %d, want 0", ts.Previous().Teams[0].Score)
	}
}

func TestStore_IgnoresNonUpdateState(t *testing.T) {
	ts := New()
	ts.Observe(bus.Event{Name: "MatchCreated"})
	ts.Observe(bus.Event{Name: "GoalScored", Raw: []byte(`{"Event":"GoalScored","Data":"{}"}`)})
	if ts.Latest() != nil {
		t.Fatal("non-UpdateState events should not populate the store")
	}
}

func TestStore_TeamByNum(t *testing.T) {
	ts := New()
	ts.Observe(updateStateTick(t, "G1", 3, 5, false))
	if got := ts.TeamByNum(0); got == nil || got.Score != 3 {
		t.Fatalf("TeamByNum(0) = %+v", got)
	}
	if got := ts.TeamByNum(1); got == nil || got.Score != 5 {
		t.Fatalf("TeamByNum(1) = %+v", got)
	}
	if got := ts.TeamByNum(99); got != nil {
		t.Fatalf("TeamByNum(99) = %+v, want nil", got)
	}
}

func TestStore_InReplay(t *testing.T) {
	ts := New()
	if ts.InReplay() {
		t.Fatal("InReplay on empty store should be false")
	}
	ts.Observe(updateStateTick(t, "G1", 0, 0, true))
	if !ts.InReplay() {
		t.Fatal("InReplay should be true after a bReplay tick")
	}
	ts.Observe(updateStateTick(t, "G1", 0, 0, false))
	if ts.InReplay() {
		t.Fatal("InReplay should be false after bReplay clears")
	}
}

// updateStateTick builds a minimal UpdateState envelope. Returns the
// typed Event a state processor would receive from the pipeline (with
// Name pre-set, mirroring RLSource.enqueue).
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
