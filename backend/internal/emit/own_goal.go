package emit

import (
	"encoding/json"
	"rl-toolkit/backend/internal/bus"
	"rl-toolkit/backend/internal/types"
	"sync"
)

// OwnGoal detects own goals via score-delta heuristic and emits
// _OwnGoal. On every UpdateState: if a team's score went up by 1 and
// the most recent BallHit was by an opposing-team player, the
// deflector own-goaled. Phase-gated to PhaseLive/PhaseReplay so
// forfeits and mercy-rule score-ups don't false-positive.
//
// Also owns the per-player honest-goal counter (realGoalsByID), bumped
// by Goal on every non-own-goal score and read by Statfeed for
// HatTrick suppression — "is this a real goal?" is this detector's
// authoritative question.
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

		// Most recent ball touch within ~5 events of the score change.
		// Anything older isn't credibly the deflection.
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

		// Optional same-window _GoalScored — RL fires GoalScored
		// alongside the score delta in most cases.
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
