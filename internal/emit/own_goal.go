package emit

import (
	"encoding/json"
	"rl-toolkit/internal/bus"
	"rl-toolkit/internal/types"
	"sync"
)

// OwnGoal detects own goals via the score-delta heuristic and emits
// _OwnGoal. Runs on UpdateState ticks: when a team's score went up
// by 1 since the previous tick AND the most recent BallHit was by
// an opposing-team player, the deflector own-goaled.
//
// Phase gate: only fires during real gameplay (PhaseLive / PhaseReplay)
// so a forfeit or mercy-rule score-up doesn't false-positive.
//
// Reads tick state from TickHistory (prev/latest team scores) and the
// most recent ball-touch from the shared Correlator.
//
// Per the design spec, OwnGoal also owns the per-player honest-goal
// counter (realGoalsByID) — bumped by Goal on every non-own-goal
// score, read by Statfeed for HatTrick suppression. The counter
// lives here (rather than at Goal) because "is this a real goal?" is
// the own-goal detector's authoritative question.
type OwnGoal struct {
	phase       PhaseGate
	ticks       TickHistory
	correlation Correlator

	realGoalsMu   sync.Mutex
	realGoalsByID map[string]int
}

func NewOwnGoal(phase PhaseGate, ticks TickHistory, correlation Correlator) *OwnGoal {
	return &OwnGoal{
		phase:         phase,
		ticks:         ticks,
		correlation:   correlation,
		realGoalsByID: make(map[string]int),
	}
}

// RealGoals returns the per-match honest-goal count for `playerID`,
// satisfying the realGoalsLookup interface Statfeed consumes for
// HatTrick suppression.
func (e *OwnGoal) RealGoals(playerID string) int {
	if playerID == "" {
		return 0
	}
	e.realGoalsMu.Lock()
	defer e.realGoalsMu.Unlock()
	return e.realGoalsByID[playerID]
}

// BumpRealGoals increments the per-player honest-goal counter. Called
// by Goal on every non-own-goal score.
func (e *OwnGoal) BumpRealGoals(playerID string) {
	if playerID == "" {
		return
	}
	e.realGoalsMu.Lock()
	defer e.realGoalsMu.Unlock()
	e.realGoalsByID[playerID]++
}

func (e *OwnGoal) Process(evt bus.Event) []bus.Event {
	if evt.Name == "MatchCreated" || evt.Name == "MatchDestroyed" {
		e.realGoalsMu.Lock()
		for k := range e.realGoalsByID {
			delete(e.realGoalsByID, k)
		}
		e.realGoalsMu.Unlock()
		return nil
	}
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
	if prev.MatchGUID != "" && curr.MatchGUID != "" && prev.MatchGUID != curr.MatchGUID {
		return nil
	}
	if e.phase != nil {
		ph := e.phase.CurrentPhase()
		if ph != types.PhaseLive && ph != types.PhaseReplay {
			return nil
		}
	}

	prevByNum := make(map[int]int, len(prev.Teams))
	for _, p := range prev.Teams {
		prevByNum[p.TeamNum] = p.Score
	}

	var emissions []bus.Event
	for _, t := range curr.Teams {
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
		var touchPlayer *types.EnrichedPlayer
		for _, p := range e.correlation.Recent("BallHit", 5) {
			if rec, ok := p.(*types.BallHitRecord); ok && rec != nil && rec.Player != nil {
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
		var correlatedScorer *types.EnrichedPlayer
		for _, p := range e.correlation.Recent("_GoalScored", 5) {
			if g, ok := p.(*types.GoalRecord); ok && g != nil {
				correlatedScorer = g.Scorer
				break
			}
		}

		body, err := json.Marshal(struct {
			MatchGUID            string                  `json:"matchGuid,omitempty"`
			Deflector            *types.EnrichedPlayer   `json:"deflector,omitempty"`
			ScoringTeam          int                     `json:"scoringTeam"`
			ConcedingTeam        int                     `json:"concedingTeam"`
			ScoreAfter           types.OwnGoalScoreAfter `json:"scoreAfter"`
			CorrelatedGoalScorer *types.EnrichedPlayer   `json:"correlatedGoalScorer,omitempty"`
		}{
			MatchGUID:     curr.MatchGUID,
			Deflector:     touchPlayer,
			ScoringTeam:   t.TeamNum,
			ConcedingTeam: touchPlayer.Team,
			ScoreAfter: types.OwnGoalScoreAfter{
				Blue:   types.TeamScore(curr.Teams, 0),
				Orange: types.TeamScore(curr.Teams, 1),
			},
			CorrelatedGoalScorer: correlatedScorer,
		})
		if err != nil {
			continue
		}
		emissions = append(emissions, bus.Event{Name: "_OwnGoal", Data: body})
	}
	return emissions
}
