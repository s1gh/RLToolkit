package main

import (
	"encoding/json"
)

// OwnGoalEmitter detects own goals via the score-delta heuristic and
// emits _OwnGoal. Runs on UpdateState ticks: when a team's score went
// up by 1 since the previous tick AND the most recent BallHit was by
// an opposing-team player, the deflector own-goaled.
//
// Phase gate: only fires during real gameplay (PhaseLive / PhaseReplay)
// so a forfeit or mercy-rule score-up doesn't false-positive.
//
// Reads tick state from TickStore (prev/latest team scores) and the
// most recent ball-touch from the shared CorrelationBuffer. Both are
// owned by their respective producers (TickStore observes
// UpdateState, BallHitEmitter records to correlation), so this
// emitter holds no per-match state of its own.
//
// Per the design spec, emit_own_goal will eventually own
// `realGoalsByID` (incremented on every non-own-goal score) and
// expose RealGoals(playerID) for emit_statfeed's hat-trick
// suppression. That coupling lands when emit_goal extracts; until
// then the legacy synthesizer keeps the counter and OwnGoalEmitter
// confines itself to score-delta detection.
type OwnGoalEmitter struct {
	matchState  *MatchState
	ticks       *TickStore
	correlation *CorrelationBuffer
}

func NewOwnGoalEmitter(ms *MatchState, ticks *TickStore, correlation *CorrelationBuffer) *OwnGoalEmitter {
	return &OwnGoalEmitter{matchState: ms, ticks: ticks, correlation: correlation}
}

func (e *OwnGoalEmitter) Process(evt Event) []Event {
	if evt.Name != "UpdateState" {
		return nil
	}
	prev := e.ticks.Previous()
	curr := e.ticks.Latest()
	if prev == nil || curr == nil {
		return nil
	}
	// Different-match guard: a guid change is a fresh baseline, not
	// a score delta.
	if prev.matchGUID != "" && curr.matchGUID != "" && prev.matchGUID != curr.matchGUID {
		return nil
	}
	if e.matchState != nil {
		ph := e.matchState.Snapshot().Phase
		if ph != PhaseLive && ph != PhaseReplay {
			return nil
		}
	}

	prevByNum := make(map[int]int, len(prev.teams))
	for _, p := range prev.teams {
		prevByNum[p.TeamNum] = p.Score
	}

	var emissions []Event
	for _, t := range curr.teams {
		oldScore, ok := prevByNum[t.TeamNum]
		if !ok {
			continue
		}
		if t.Score-oldScore != 1 {
			continue
		}

		// Most recent ball touch — within ~5 events of the score
		// change. Anything older isn't credible as the deflection
		// that crossed the line.
		var touchPlayer *EnrichedPlayer
		for _, p := range e.correlation.Recent("BallHit", 5) {
			if rec, ok := p.(*ballHitRecord); ok && rec != nil && rec.Player != nil {
				touchPlayer = rec.Player
				break
			}
		}
		if touchPlayer == nil || touchPlayer.Team == t.TeamNum {
			continue
		}

		// Optional same-window correlated _GoalScored payload — RL
		// fires GoalScored alongside the score delta in most cases,
		// so the lookup almost always hits.
		var correlatedScorer *EnrichedPlayer
		for _, p := range e.correlation.Recent("_GoalScored", 5) {
			if g, ok := p.(*goalRecord); ok && g != nil {
				correlatedScorer = g.Scorer
				break
			}
		}

		body, err := json.Marshal(struct {
			MatchGUID            string            `json:"matchGuid,omitempty"`
			Deflector            *EnrichedPlayer   `json:"deflector,omitempty"`
			ScoringTeam          int               `json:"scoringTeam"`
			ConcedingTeam        int               `json:"concedingTeam"`
			ScoreAfter           ownGoalScoreAfter `json:"scoreAfter"`
			CorrelatedGoalScorer *EnrichedPlayer   `json:"correlatedGoalScorer,omitempty"`
		}{
			MatchGUID:     curr.matchGUID,
			Deflector:     touchPlayer,
			ScoringTeam:   t.TeamNum,
			ConcedingTeam: touchPlayer.Team,
			ScoreAfter: ownGoalScoreAfter{
				Blue:   teamScore(curr.teams, 0),
				Orange: teamScore(curr.teams, 1),
			},
			CorrelatedGoalScorer: correlatedScorer,
		})
		if err != nil {
			continue
		}
		emissions = append(emissions, Event{Name: "_OwnGoal", Data: body})
	}
	return emissions
}
