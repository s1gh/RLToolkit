package backend

import (
	"encoding/json"
	"rl-toolkit/internal/bus"
	"testing"
)

// names returns the Name field of every event in evts. Shared across
// the emit-processor tests as a compact way to render expected /
// actual sequences in failure messages.
func names(evts []bus.Event) []string {
	out := make([]string, len(evts))
	for i, e := range evts {
		out[i] = e.Name
	}
	return out
}

// hasName reports whether any event in evts is named name. Convenience
// wrapper that the demos / statfeed tests use to assert "this event
// fired" without constructing a full slice equality.
func hasName(evts []bus.Event, name string) bool {
	for _, e := range evts {
		if e.Name == name {
			return true
		}
	}
	return false
}

// rosterPlayerStub is a compact way for in-backend emit tests
// (currently emit_goal, emit_own_goal, emit_statfeed) to seed a
// RosterTracker from a name/team list.
type rosterPlayerStub struct {
	ID   string
	Name string
	Team int
}

// updateStateTick builds a minimal UpdateState envelope. Returns the
// typed Event a state processor would receive from the pipeline (with
// Name pre-set, mirroring RLSource.enqueue). Shared across the
// in-backend emit tests (own_goal, goal) that drive a real TickStore.
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

// makeUpdateStateRoster builds an UpdateState envelope just rich
// enough for RosterTracker to ingest the given player list.
func makeUpdateStateRoster(t *testing.T, players []rosterPlayerStub) []byte {
	t.Helper()
	wirePlayers := make([]map[string]any, 0, len(players))
	for _, p := range players {
		wirePlayers = append(wirePlayers, map[string]any{
			"PrimaryId": p.ID,
			"Name":      p.Name,
			"TeamNum":   p.Team,
		})
	}
	inner, _ := json.Marshal(map[string]any{
		"MatchGuid": "G",
		"Players":   wirePlayers,
	})
	raw, _ := json.Marshal(map[string]any{"Event": "UpdateState", "Data": string(inner)})
	return raw
}
