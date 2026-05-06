package main

import (
	"encoding/json"
	"testing"
)

func TestBallHitEmitter_RepublishesWithEnrichedPlayers(t *testing.T) {
	roster := NewRosterTracker(NewBus())
	// Seed a UpdateState so ResolveByShortcut can find a name match.
	roster.Observe(Event{Name: "UpdateState", Raw: makeUpdateStateRoster(t, []rosterPlayerStub{{ID: "Steam|7|0", Name: "Ada", Team: 1}})})

	e := NewBallHitEmitter(roster, nil)
	evt := makeBallHit(t, "G", "Ada", 65, 110)
	out := e.Process(evt)
	if len(out) != 1 || out[0].Name != "_BallHit" {
		t.Fatalf("expected 1 _BallHit, got %v", out)
	}
	var body struct {
		Players      []*EnrichedPlayer `json:"players"`
		PreHitSpeed  *float64          `json:"preHitSpeed"`
		PostHitSpeed *float64          `json:"postHitSpeed"`
	}
	if err := json.Unmarshal(out[0].Data, &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(body.Players) != 1 {
		t.Fatalf("want 1 player, got %d", len(body.Players))
	}
	if body.Players[0].ID != "Steam|7|0" || body.Players[0].Name != "Ada" {
		t.Fatalf("player not resolved against roster: %+v", body.Players[0])
	}
	if body.PreHitSpeed == nil || *body.PreHitSpeed != 65 ||
		body.PostHitSpeed == nil || *body.PostHitSpeed != 110 {
		t.Fatalf("speeds dropped: %+v / %+v", body.PreHitSpeed, body.PostHitSpeed)
	}
}

func TestBallHitEmitter_PhaseGated(t *testing.T) {
	roster := NewRosterTracker(nil)
	ms := NewMatchState()
	// MatchState defaults to PhaseNone; emitter must skip.
	e := NewBallHitEmitter(roster, ms)
	if got := e.Process(makeBallHit(t, "G", "Ada", 1, 2)); len(got) != 0 {
		t.Fatalf("PhaseNone should skip, got %v", got)
	}
}

func TestBallHitEmitter_IgnoresNonBallHit(t *testing.T) {
	e := NewBallHitEmitter(NewRosterTracker(nil), nil)
	if got := e.Process(Event{Name: "Other", Raw: []byte(`{}`)}); len(got) != 0 {
		t.Fatalf("non-BallHit should be ignored, got %v", got)
	}
}

func makeBallHit(t *testing.T, guid, playerName string, pre, post float64) Event {
	t.Helper()
	inner, _ := json.Marshal(map[string]any{
		"MatchGuid": guid,
		"Players": []map[string]any{
			{"Name": playerName, "Shortcut": 0, "TeamNum": 1},
		},
		"Ball": map[string]any{
			"PreHitSpeed":  pre,
			"PostHitSpeed": post,
			"Location":     map[string]any{"x": 1.0, "y": 2.0, "z": 3.0},
		},
	})
	raw, _ := json.Marshal(map[string]any{"Event": "BallHit", "Data": string(inner)})
	return Event{Name: "BallHit", Raw: raw}
}

type rosterPlayerStub struct {
	ID   string
	Name string
	Team int
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
