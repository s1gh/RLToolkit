package backend

import "encoding/json"

// MatchEndedEmitter republishes RL's MatchEnded as _MatchEnded with
// final scores + winner name resolved from the cached UpdateState.
// Subscribers don't have to keep their own tick state to render the
// post-match line.
type MatchEndedEmitter struct {
	ticks *TickStore
}

func NewMatchEndedEmitter(ticks *TickStore) *MatchEndedEmitter {
	return &MatchEndedEmitter{ticks: ticks}
}

func (e *MatchEndedEmitter) Process(evt Event) []Event {
	if evt.Name != "MatchEnded" {
		return nil
	}
	inner := unwrapInnerData(evt.Raw)
	if inner == "" {
		return nil
	}
	var d matchEndedData
	if err := json.Unmarshal([]byte(inner), &d); err != nil {
		return nil
	}
	guid := pickStr(d.MatchGUID, d.MatchGUIDLow)
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
	if blue := e.ticks.TeamByNum(0); blue != nil {
		score := blue.Score
		payload.ScoreBlue = &score
	}
	if orange := e.ticks.TeamByNum(1); orange != nil {
		score := orange.Score
		payload.ScoreOrange = &score
	}
	if winner != nil {
		if t := e.ticks.TeamByNum(*winner); t != nil {
			payload.WinnerName = t.Name
		}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil
	}
	return []Event{{Name: "_MatchEnded", Data: body}}
}
