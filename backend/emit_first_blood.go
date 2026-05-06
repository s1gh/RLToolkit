package main

import (
	"encoding/json"
	"time"
)

// FirstBloodEmitter publishes three "fire once per match" milestones:
//
//   - _FirstTouch on the first _BallHit after each RoundStarted (re-arms
//     every round; each goal ends with a kickoff, so the next round
//     gets its own _FirstTouch).
//   - _FirstBlood on the first _GoalScored of the match.
//   - _OvertimeStarted on the rising edge of UpdateState.Game.bOvertime.
//
// All three reset on MatchCreated / MatchDestroyed so a back-to-back
// rematch into the same lobby starts fresh.
//
// No mutex: emit processors run single-threaded inside Pipeline.Run.
type FirstBloodEmitter struct {
	awaitingFirstTouch   bool
	firstBloodFired      bool
	overtimeStartedFired bool
	wasOvertime          bool
	matchInitializedAt   time.Time
	roundStartedAt       time.Time
}

func NewFirstBloodEmitter() *FirstBloodEmitter { return &FirstBloodEmitter{} }

func (e *FirstBloodEmitter) Process(evt Event) []Event {
	switch evt.Name {
	case "MatchCreated", "MatchDestroyed":
		*e = FirstBloodEmitter{}
		return nil
	case "MatchInitialized":
		e.matchInitializedAt = time.Now()
		return nil
	case "RoundStarted":
		e.awaitingFirstTouch = true
		e.roundStartedAt = time.Now()
		return nil
	case "_BallHit":
		return e.emitFirstTouch(evt)
	case "_GoalScored":
		return e.emitFirstBlood(evt)
	case "UpdateState":
		return e.emitOvertime(evt)
	}
	return nil
}

func (e *FirstBloodEmitter) emitFirstTouch(evt Event) []Event {
	if !e.awaitingFirstTouch {
		return nil
	}
	e.awaitingFirstTouch = false
	roundStart := e.roundStartedAt

	var p struct {
		MatchGUID    string            `json:"matchGuid"`
		Players      []*EnrichedPlayer `json:"players"`
		PostHitSpeed *float64          `json:"postHitSpeed"`
		Location     *vec3             `json:"location"`
	}
	if err := json.Unmarshal(payloadBytes(evt), &p); err != nil {
		return nil
	}
	var elapsed *float64
	if !roundStart.IsZero() {
		dur := time.Since(roundStart).Seconds()
		elapsed = &dur
	}
	body, err := json.Marshal(struct {
		MatchGUID                   string            `json:"matchGuid,omitempty"`
		Players                     []*EnrichedPlayer `json:"players"`
		PostHitSpeed                *float64          `json:"postHitSpeed,omitempty"`
		Location                    *vec3             `json:"location,omitempty"`
		TimeFromCountdownEndSeconds *float64          `json:"timeFromCountdownEndSeconds,omitempty"`
	}{
		MatchGUID:                   p.MatchGUID,
		Players:                     p.Players,
		PostHitSpeed:                p.PostHitSpeed,
		Location:                    p.Location,
		TimeFromCountdownEndSeconds: elapsed,
	})
	if err != nil {
		return nil
	}
	return []Event{{Name: "_FirstTouch", Data: body}}
}

func (e *FirstBloodEmitter) emitFirstBlood(evt Event) []Event {
	if e.firstBloodFired {
		return nil
	}
	e.firstBloodFired = true
	matchStart := e.matchInitializedAt

	var p struct {
		MatchGUID     string          `json:"matchGuid"`
		Scorer        *EnrichedPlayer `json:"scorer"`
		ScoringTeam   *int            `json:"scoringTeam"`
		ConcedingTeam *int            `json:"concedingTeam"`
	}
	if err := json.Unmarshal(payloadBytes(evt), &p); err != nil {
		return nil
	}
	var secondsIn *float64
	if !matchStart.IsZero() {
		dur := time.Since(matchStart).Seconds()
		secondsIn = &dur
	}
	scoringTeam := 0
	concedingTeam := 0
	if p.ScoringTeam != nil {
		scoringTeam = *p.ScoringTeam
	}
	if p.ConcedingTeam != nil {
		concedingTeam = *p.ConcedingTeam
	}
	body, err := json.Marshal(struct {
		MatchGUID        string          `json:"matchGuid,omitempty"`
		Scorer           *EnrichedPlayer `json:"scorer"`
		ScoringTeam      int             `json:"scoringTeam"`
		ConcedingTeam    int             `json:"concedingTeam"`
		SecondsIntoMatch *float64        `json:"secondsIntoMatch,omitempty"`
	}{
		MatchGUID:        p.MatchGUID,
		Scorer:           p.Scorer,
		ScoringTeam:      scoringTeam,
		ConcedingTeam:    concedingTeam,
		SecondsIntoMatch: secondsIn,
	})
	if err != nil {
		return nil
	}
	return []Event{{Name: "_FirstBlood", Data: body}}
}

// emitOvertime detects the rising edge of bOvertime within UpdateState.
// We parse the inner Data string ourselves (RL's UpdateState wraps its
// payload as a JSON-encoded string) rather than depending on
// TickStore — Stage 5.7 will move the per-tick decode into a shared
// store, but until then this emitter pulls only what it needs.
func (e *FirstBloodEmitter) emitOvertime(evt Event) []Event {
	inner := unwrapInnerData(evt.Raw)
	if inner == "" {
		return nil
	}
	var d struct {
		Game *struct {
			BOvertime bool `json:"bOvertime"`
		} `json:"Game"`
		MatchGUID    string     `json:"MatchGuid"`
		MatchGUIDLow string     `json:"matchGuid"`
		Teams        []wireTeam `json:"Teams"`
		TeamsLow     []wireTeam `json:"teams"`
	}
	if err := json.Unmarshal([]byte(inner), &d); err != nil || d.Game == nil {
		return nil
	}

	prev := e.wasOvertime
	e.wasOvertime = d.Game.BOvertime
	if prev || !d.Game.BOvertime || e.overtimeStartedFired {
		return nil
	}
	e.overtimeStartedFired = true
	matchStart := e.matchInitializedAt

	teams := d.Teams
	if len(teams) == 0 {
		teams = d.TeamsLow
	}
	scoreBlue, scoreOrange := 0, 0
	for _, t := range teams {
		switch t.TeamNum {
		case 0:
			scoreBlue = t.Score
		case 1:
			scoreOrange = t.Score
		}
	}
	var matchDur *float64
	if !matchStart.IsZero() {
		dur := time.Since(matchStart).Seconds()
		matchDur = &dur
	}
	guid := d.MatchGUID
	if guid == "" {
		guid = d.MatchGUIDLow
	}
	body, err := json.Marshal(struct {
		MatchGUID                    string   `json:"matchGuid,omitempty"`
		ScoreBlue                    int      `json:"scoreBlue"`
		ScoreOrange                  int      `json:"scoreOrange"`
		TiedAt                       int      `json:"tiedAt"`
		MatchDurationSecondsBeforeOT *float64 `json:"matchDurationSecondsBeforeOT,omitempty"`
	}{
		MatchGUID:                    guid,
		ScoreBlue:                    scoreBlue,
		ScoreOrange:                  scoreOrange,
		TiedAt:                       scoreBlue,
		MatchDurationSecondsBeforeOT: matchDur,
	})
	if err != nil {
		return nil
	}
	return []Event{{Name: "_OvertimeStarted", Data: body}}
}
