package emit

import (
	"encoding/json"
	"rl-toolkit/backend/internal/bus"
	"rl-toolkit/backend/internal/correlation"
	"rl-toolkit/backend/internal/types"
	"testing"
)

// rosterStub returns a fully-resolved EnrichedPlayer per name. The
// real RosterTracker does roster joins; for emitter unit tests the
// stub is enough.
type rosterStub struct {
	players map[string]*types.EnrichedPlayer
}

func (r rosterStub) ResolveByShortcut(ref types.ShortcutRef) *types.EnrichedPlayer {
	if p, ok := r.players[ref.Name]; ok {
		return p
	}
	return &types.EnrichedPlayer{Name: ref.Name, Team: ref.TeamNum}
}

// stubPhase returns a fixed Phase. Used to gate emit tests through
// the live / non-live transition without spinning up MatchState.
type stubPhase struct{ phase types.Phase }

func (s stubPhase) CurrentPhase() types.Phase { return s.phase }

func TestBallHit_RepublishesWithEnrichedPlayers(t *testing.T) {
	roster := rosterStub{players: map[string]*types.EnrichedPlayer{
		"Ada": {ID: "Steam|7|0", Name: "Ada", Team: 1},
	}}
	corr := correlation.New(8)
	e := NewBallHit(roster, nil, corr)

	out := e.Process(makeBallHit(t, "G", "Ada", 65, 110))
	if len(out) != 1 || out[0].Name != "_BallHit" {
		t.Fatalf("expected 1 _BallHit, got %v", out)
	}
	var body struct {
		Players      []*types.EnrichedPlayer `json:"players"`
		PreHitSpeed  *float64                `json:"preHitSpeed"`
		PostHitSpeed *float64                `json:"postHitSpeed"`
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

	// Correlation buffer should hold the touch for downstream emitters.
	if got := corr.Recent("BallHit", 1); len(got) != 1 {
		t.Errorf("expected BallHit in correlation buffer, got %v", got)
	}
}

func TestBallHit_PhaseGated(t *testing.T) {
	e := NewBallHit(rosterStub{}, stubPhase{phase: types.PhaseNone}, correlation.New(8))
	if got := e.Process(makeBallHit(t, "G", "Ada", 1, 2)); len(got) != 0 {
		t.Fatalf("PhaseNone should skip, got %v", got)
	}
}

func TestBallHit_IgnoresNonBallHit(t *testing.T) {
	e := NewBallHit(rosterStub{}, nil, correlation.New(8))
	if got := e.Process(bus.Event{Name: "Other", Raw: []byte(`{}`)}); len(got) != 0 {
		t.Fatalf("non-BallHit should be ignored, got %v", got)
	}
}

func makeBallHit(t *testing.T, guid, playerName string, pre, post float64) bus.Event {
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
	return bus.Event{Name: "BallHit", Raw: raw}
}
