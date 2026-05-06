package main

import (
	"encoding/json"
	"sync"
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
// Why parse `evt.Raw` for the upstream synthetic events: while the
// legacy Synthesizer is still the producer of `_BallHit` and
// `_GoalScored`, those events arrive on the wire as flat JSON
// (everything inline alongside `"Event"`) and `evt.Data` is empty.
// `payloadBytes` reads from whichever side is populated.
type FirstBloodEmitter struct {
	mu                   sync.Mutex
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
		e.reset()
		return nil
	case "MatchInitialized":
		e.mu.Lock()
		e.matchInitializedAt = time.Now()
		e.mu.Unlock()
		return nil
	case "RoundStarted":
		e.mu.Lock()
		e.awaitingFirstTouch = true
		e.roundStartedAt = time.Now()
		e.mu.Unlock()
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

func (e *FirstBloodEmitter) reset() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.awaitingFirstTouch = false
	e.firstBloodFired = false
	e.overtimeStartedFired = false
	e.wasOvertime = false
	e.matchInitializedAt = time.Time{}
	e.roundStartedAt = time.Time{}
}

func (e *FirstBloodEmitter) emitFirstTouch(evt Event) []Event {
	e.mu.Lock()
	if !e.awaitingFirstTouch {
		e.mu.Unlock()
		return nil
	}
	e.awaitingFirstTouch = false
	roundStart := e.roundStartedAt
	e.mu.Unlock()

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
	e.mu.Lock()
	if e.firstBloodFired {
		e.mu.Unlock()
		return nil
	}
	e.firstBloodFired = true
	matchStart := e.matchInitializedAt
	e.mu.Unlock()

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

	e.mu.Lock()
	prev := e.wasOvertime
	e.wasOvertime = d.Game.BOvertime
	if prev || !d.Game.BOvertime || e.overtimeStartedFired {
		e.mu.Unlock()
		return nil
	}
	e.overtimeStartedFired = true
	matchStart := e.matchInitializedAt
	e.mu.Unlock()

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
