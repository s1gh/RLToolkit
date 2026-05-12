package emit

import (
	"encoding/json"
	"rl-toolkit/backend/internal/bus"
	"rl-toolkit/backend/internal/types"
	"rl-toolkit/backend/internal/wire"
)

// MatchEnded republishes RL's MatchEnded as _MatchEnded with final
// scores + winner name resolved from the cached UpdateState.
// Subscribers don't have to keep their own tick state to render the
// post-match line.
//
// When RL omits WinnerTeamNum (forfeit, disconnect, server kick) the
// emitter derives the winner from the cached team scores and ships
// it on the same field, so plugins always see a populated
// winnerTeamNum whenever the backend can determine a winner. Tied
// scores stay null — RL doesn't end clean matches in ties, so a tie
// at end-of-match means data is incomplete and we won't guess.
type MatchEnded struct {
	teams TeamReader
}

func NewMatchEnded(teams TeamReader) *MatchEnded {
	return &MatchEnded{teams: teams}
}

func (e *MatchEnded) Process(evt bus.Event) []bus.Event {
	if evt.Name != "MatchEnded" {
		return nil
	}
	inner := wire.UnwrapInnerData(evt.Raw)
	if inner == "" {
		return nil
	}
	var d types.MatchEndedData
	if err := json.Unmarshal([]byte(inner), &d); err != nil {
		return nil
	}
	guid := wire.PickStr(d.MatchGUID, d.MatchGUIDLow)
	winner := d.WinnerTeamNum
	if winner == nil {
		winner = d.WinnerTeamLow
	}

	payload := struct {
		MatchGUID     string `json:"matchGuid,omitempty"`
		WinnerTeamNum *int   `json:"winnerTeamNum,omitempty"`
		WinnerName    string `json:"winnerName,omitempty"`
		ScoreBlue     *int   `json:"scoreBlue,omitempty"`
		ScoreOrange   *int   `json:"scoreOrange,omitempty"`
	}{
		MatchGUID:     guid,
		WinnerTeamNum: winner,
	}
	if blue := e.teams.TeamByNum(0); blue != nil {
		score := blue.Score
		payload.ScoreBlue = &score
	}
	if orange := e.teams.TeamByNum(1); orange != nil {
		score := orange.Score
		payload.ScoreOrange = &score
	}
	// Score-derive the winner when RL didn't provide one. Only valid
	// when both team scores are present and unequal; ties stay null.
	if winner == nil && payload.ScoreBlue != nil && payload.ScoreOrange != nil && *payload.ScoreBlue != *payload.ScoreOrange {
		var derived int
		if *payload.ScoreBlue > *payload.ScoreOrange {
			derived = 0
		} else {
			derived = 1
		}
		winner = &derived
		payload.WinnerTeamNum = winner
	}
	if winner != nil {
		if t := e.teams.TeamByNum(*winner); t != nil {
			payload.WinnerName = t.Name
		}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil
	}
	return []bus.Event{{Name: "_MatchEnded", Data: body}}
}
