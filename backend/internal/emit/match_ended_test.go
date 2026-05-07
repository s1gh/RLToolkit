package emit

import (
	"encoding/json"
	"rl-toolkit/backend/internal/bus"
	"rl-toolkit/backend/internal/types"
	"testing"
)

// stubTeams returns the given teams from TeamByNum lookups. Mirrors
// what TickStore exposes in production.
type stubTeams struct {
	teams map[int]types.TeamRef
}

func (s stubTeams) TeamByNum(num int) *types.TeamRef {
	if t, ok := s.teams[num]; ok {
		return &t
	}
	return nil
}

func TestMatchEnded_PublishesScoresAndWinner(t *testing.T) {
	teams := stubTeams{teams: map[int]types.TeamRef{
		0: {TeamNum: 0, Name: "Blue", Score: 3},
		1: {TeamNum: 1, Name: "Orange", Score: 2},
	}}
	e := NewMatchEnded(teams)

	winner := 0
	winnerJSON, _ := json.Marshal(winner)
	inner, _ := json.Marshal(map[string]any{
		"MatchGuid":     "G1",
		"WinnerTeamNum": json.RawMessage(winnerJSON),
	})
	raw, _ := json.Marshal(map[string]any{"Event": "MatchEnded", "Data": string(inner)})
	out := e.Process(bus.Event{Name: "MatchEnded", Raw: raw})
	if len(out) != 1 || out[0].Name != "_MatchEnded" {
		t.Fatalf("expected _MatchEnded, got %v", out)
	}

	var payload struct {
		MatchGUID     string `json:"matchGuid"`
		WinnerTeamNum *int   `json:"winnerTeamNum"`
		WinnerName    string `json:"winnerName"`
		ScoreBlue     *int   `json:"scoreBlue"`
		ScoreOrange   *int   `json:"scoreOrange"`
	}
	if err := json.Unmarshal(out[0].Data, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.MatchGUID != "G1" {
		t.Errorf("guid: got %q", payload.MatchGUID)
	}
	if payload.WinnerTeamNum == nil || *payload.WinnerTeamNum != 0 {
		t.Errorf("winnerTeamNum: got %v", payload.WinnerTeamNum)
	}
	if payload.WinnerName != "Blue" {
		t.Errorf("winnerName: got %q", payload.WinnerName)
	}
	if payload.ScoreBlue == nil || *payload.ScoreBlue != 3 {
		t.Errorf("scoreBlue: got %v", payload.ScoreBlue)
	}
	if payload.ScoreOrange == nil || *payload.ScoreOrange != 2 {
		t.Errorf("scoreOrange: got %v", payload.ScoreOrange)
	}
}

func TestMatchEnded_IgnoresOtherEvents(t *testing.T) {
	e := NewMatchEnded(stubTeams{})
	if got := e.Process(bus.Event{Name: "Other"}); len(got) != 0 {
		t.Fatalf("non-MatchEnded should be ignored, got %v", got)
	}
}

// TestMatchEnded_HandlesMissingTeams confirms the payload still ships
// when TickStore hasn't yet seen an UpdateState — score fields drop
// to omitempty and the consumer just sees the winner reference.
func TestMatchEnded_HandlesMissingTeams(t *testing.T) {
	e := NewMatchEnded(stubTeams{teams: map[int]types.TeamRef{}})
	inner, _ := json.Marshal(map[string]any{"MatchGuid": "G2"})
	raw, _ := json.Marshal(map[string]any{"Event": "MatchEnded", "Data": string(inner)})
	out := e.Process(bus.Event{Name: "MatchEnded", Raw: raw})
	if len(out) != 1 {
		t.Fatalf("expected 1 emission, got %v", out)
	}
	body := string(out[0].Data)
	if body != `{"matchGuid":"G2"}` {
		t.Errorf("expected only matchGuid in payload, got %s", body)
	}
}
