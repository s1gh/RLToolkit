// Package emit holds the pipeline's pure synthetic-event producers.
// Each emitter implements pipeline.EmitProcessor (one Process method)
// and stays free of internal mutex state — the pipeline runs them
// single-threaded so any per-match counters live as plain fields.
//
// Cross-package dependencies are expressed as small consumer-side
// interfaces (TickReader, RosterResolver, ReplayGate) so this package
// stays free of any backend coupling.
package emit

import (
	"rl-toolkit/backend/internal/bus"
	"rl-toolkit/backend/internal/types"
)

// RosterResolver is the slim view emitters need into the roster
// tracker: turn a {Name, Shortcut, TeamNum} stub into a fully-enriched
// player. The roster tracker in backend satisfies this structurally.
type RosterResolver interface {
	ResolveByShortcut(ref types.ShortcutRef) *types.EnrichedPlayer
}

// ReplayGate exposes the single bit of TickStore state emitters need
// for phase-gated suppression: are we currently in a goal replay?
// (RL keeps firing some envelopes during the cinematic.)
type ReplayGate interface {
	InReplay() bool
}

// TickHistory is the diff view goal/own-goal/score-diff emitters need
// into TickStore: the (prev, latest) pair of TickSnapshots used to
// detect what changed between two ticks.
type TickHistory interface {
	Latest() *types.TickSnapshot
	Previous() *types.TickSnapshot
}

// TeamReader is the slim view emitters need into the most recent
// UpdateState's Teams[] cache: pull a team's score / name / colors
// by TeamNum, returning nil if no tick has arrived or the team isn't
// present.
type TeamReader interface {
	TeamByNum(num int) *types.TeamRef
}

// PhaseGate is the slim view emitters need into MatchState for
// gameplay-only suppression: query the current phase and gate emission
// on PhaseLive / PhaseCountdown / PhasePaused. Returning the full
// Snapshot would couple emit/ to internal/state, so we just hand back
// the Phase string.
type PhaseGate interface {
	CurrentPhase() types.Phase
}

// Correlator is the slim view emitters need into the shared
// CorrelationBuffer: stash a typed entry (BallHitRecord, GoalRecord,
// StatfeedRecord, etc.) under a string key, look it back up via Recent
// or FindWithin. correlation.Buffer satisfies this.
type Correlator interface {
	Record(name string, payload interface{})
	Recent(name string, n int) []interface{}
	FindWithin(name string, lookback int, predicate func(interface{}) bool) interface{}
	RemoveByName(name string, predicate func(interface{}) bool)
}

// FlipResetClearer is the slim view TickDiff needs into Statfeed:
// when a player's bOnGround flag rises, clear any flip-reset arm so
// the next goal from that player isn't tagged unless they earn
// another reset run.
type FlipResetClearer interface {
	ClearFlipResetArm(playerID string)
}

// FlipResetConsumer is the slim view Goal needs into Statfeed: when
// the scorer is armed for a flip-reset goal, consume the arm and
// stamp IsFlipResetGoal.
type FlipResetConsumer interface {
	ConsumeFlipResetArm(playerID string) bool
}

// GoalCounter is the slim view Goal needs into OwnGoal: bump the
// per-player honest-goal counter on every non-own-goal score, and
// look it up to gate _HatTrick emission at the third real goal.
type GoalCounter interface {
	RealGoals(playerID string) int
	BumpRealGoals(playerID string)
}

// DiscoveryRecorder is the slim view Statfeed needs from the
// statfeed-discoveries store: stash a newly-seen EventName so
// authors can confirm it before adding to the verified registry.
// The bool return reports whether the observation was the first
// time this name was seen; emit doesn't use it but the production
// store reports it for log throttling.
type DiscoveryRecorder interface {
	Record(name string) bool
}

// liveGameplayPhases gates BallHit / OwnGoal / Statfeed emitters that
// must not fire during goal replays or post-match screens.
func liveGameplayPhases(g PhaseGate) bool {
	if g == nil {
		return true
	}
	ph := g.CurrentPhase()
	return ph == types.PhaseLive || ph == types.PhaseCountdown || ph == types.PhasePaused
}

// recentTouch looks up the most recent BallHit from the shared
// correlation buffer and returns it as a wire-ready
// EnrichedCorrelatedTouch. Used by both TickDiff (for
// _BallPossessionChanged.triggeredBy) and Goal (for the implicit
// last-touch fallback when GoalScored omits BallLastTouch).
func recentTouch(c Correlator, window int) *types.EnrichedCorrelatedTouch {
	if c == nil {
		return nil
	}
	for _, p := range c.Recent("BallHit", window) {
		rec, ok := p.(*types.BallHitRecord)
		if !ok || rec == nil || rec.Player == nil {
			continue
		}
		return &types.EnrichedCorrelatedTouch{
			Player:       rec.Player,
			PreHitSpeed:  rec.PreHitSpeed,
			PostHitSpeed: rec.PostHitSpeed,
		}
	}
	return nil
}

// payloadBytes returns Data when set (native emit processors) and
// falls back to Raw (flat-JSON producers).
func payloadBytes(evt bus.Event) []byte {
	if len(evt.Data) > 0 {
		return evt.Data
	}
	return evt.Raw
}
