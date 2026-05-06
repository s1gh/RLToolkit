package main

import (
	"encoding/json"
	"sync"
	"time"
)

// Synthesizer turns raw RL events into _-prefixed synthetic events with
// pre-resolved player references and other server-side enrichment. One
// instance is attached to the RL client alongside the trackers; Feed is
// called from the dispatcher after the roster tracker has digested the
// envelope, so player resolution sees the up-to-date roster.
type Synthesizer struct {
	bus    Broadcaster
	roster *RosterTracker
	// matchState is the unified gameplay-state machine. Consulted by
	// emitters that should only fire during real gameplay (e.g. _OwnGoal
	// — a score-delta during a forfeit or mercy rule must not flag as
	// an own goal) and by diff emitters that need to skip replays /
	// post-match screens. Optional; nil disables phase gating, which is
	// what the unit tests do.
	matchState *MatchState

	// discoveries is the persistent registry of unknown Statfeed names.
	// Optional — when set, _UnknownStatfeed observations bump the
	// per-name counter and the /api/statfeed-discoveries endpoint can
	// return them.
	discoveries *StatfeedDiscoveryStore

	// ticks is the shared per-tick decode store. Owned by main; the
	// synth's diff emitters read prev/latest from it instead of
	// caching their own copy.
	ticks *TickStore

// correlation is the shared sliding window of recent events. Owned
	// by main; processors share one instance so a producer (e.g.
	// BallHitEmitter) can record a touch and a consumer (the synth's
	// _GoalScored fallback, soon emit_own_goal) can look it back up.
	// Emitters like _GoalScored use it to look back for modifier
	// Statfeeds (AerialGoal, BackwardsGoal,
	// …) and _OwnGoal can look back for the GoalScored it belongs to.
	// Sized in events, not ticks — see CorrelationBuffer.
	correlation *CorrelationBuffer

// crossbarMu guards lastCrossbarHit. RL fires a burst of CrossbarHit
	// events when the ball rolls along the goal frame (we've seen 5 in
	// a row). Debounce so consumers see one event per real impact.
	crossbarMu      sync.Mutex
	lastCrossbarHit time.Time

	// lastGoalMu guards lastGoal. Set when _GoalScored is published,
	// read when GoalReplayStart arrives so _GoalReplayContext can ship
	// the resolved goal payload without depending on the shared
	// correlation buffer (which can evict the entry under burst load
	// from intervening BallHit/StatfeedEvent records). Holds the full
	// enriched envelope so _GoalReplayContext can mirror every field
	// _GoalScored carried (assister, ballLastTouch, modifiers, ...).
	// Cleared on match boundaries via resetMatchMilestones.
	lastGoalMu sync.Mutex
	lastGoal   *enrichedGoalScored

	// flipResetArmedMu guards flipResetArmed. Tracks "this player got a
	// FlipReset and hasn't touched the ground since" — keyed by the
	// canonical player id. Set on FlipReset statfeed, cleared the
	// instant their bOnGround flips back to true (UpdateState diff) or
	// when they consume it by scoring. A goal scored while still in
	// this state is tagged Modifiers.IsFlipResetGoal.
	flipResetArmedMu sync.Mutex
	flipResetArmed   map[string]bool

	// realGoalsMu guards realGoalsByID. Counts goals where the scorer
	// actually scored on the opposing net (i.e. not an own goal),
	// keyed by canonical player id. Used to suppress RL's HatTrick
	// Statfeed when the threshold was reached only because RL counts
	// own goals — we don't. Cleared on match boundaries.
	realGoalsMu   sync.Mutex
	realGoalsByID map[string]int

	// flipResetCountMu guards flipResetCountByID. Per-player count of
	// FlipReset Statfeeds observed in the current match, keyed by
	// canonical player id. Stamped on _FlipReset.flipResetsThisMatch
	// for parity with _HatTrick.goalsThisMatch. Cleared on match
	// boundaries.
	flipResetCountMu   sync.Mutex
	flipResetCountByID map[string]int

// Match summary settle-window state. Set on MatchEnded; cleared
	// when _MatchSummary publishes (settle timeout, PodiumStart, or
	// MatchDestroyed — whichever first).
	summaryMu        sync.Mutex
	summaryPending   bool
	summaryGUID      string
	summaryWinner    *int
	summaryFinalSnap *tickSnapshot
	summaryMVP       *EnrichedPlayer
	summaryCancel    chan struct{}
	// summaryFlushed lets _MatchMVP report whether the late MVP
	// statfeed arrived before or after _MatchSummary published. Reset
	// on MatchEnded (beginMatchSummary).
	summaryFlushed bool
	// summaryMatchEndedAt timestamps the most recent MatchEnded so
	// _MatchMVP.secondsAfterMatchEnd is computable.
	summaryMatchEndedAt time.Time
	// Tunable timeouts; defaults to the production constants. Tests
	// override to 0 (or near-zero) so the suite doesn't wait seconds.
	summaryEndedTimeout  time.Duration
	summaryPodiumSettle  time.Duration

	// Per-match log of _PlayerDemolished envelopes (raw bytes, ready to
	// rewrite to a fresh SSE subscriber). Reset on MatchCreated / first
	// MatchInitialized of a new guid and on MatchDestroyed. Plugins
	// reloading mid-match (e.g. dashboard refresh, OBS source reload)
	// get the full demo history replayed on subscribe so per-match
	// counters in the demos plugin survive page reloads without each
	// plugin needing its own persistence layer.
	demolishMu  sync.Mutex
	demolishLog [][]byte

	// demoChainMu guards demoChainByID. Per-attacker rolling list of
	// recent demo timestamps for _DemoChain detection. Trimmed to the
	// window each time we observe a demo. Cleared on match boundaries.
	demoChainMu   sync.Mutex
	demoChainByID map[string][]demoChainEntry

// kickoffMu guards kickoff conversion state. roundStartedAtKickoff
	// is set on RoundStarted; cleared once _KickoffConverted has fired
	// for that round (or when the next RoundStarted resets it).
	kickoffMu              sync.Mutex
	kickoffWindowDeadline  time.Time
	kickoffConvertedFired  bool
}

// demoChainEntry pairs a demo timestamp with the resolved victim, so
// _DemoChain can report `victims[]` for the chain so far.
type demoChainEntry struct {
	at     time.Time
	victim *EnrichedPlayer
}

// demoChainWindow is the sliding window within which back-to-back demos
// by the same attacker count as a chain. 5s is wide enough to cover a
// player chasing across the field, narrow enough that the next match's
// first demo doesn't extend a stale chain.
const demoChainWindow = 5 * time.Second

// kickoffWindow caps how long after RoundStarted a same-team _Shot or
// _GoalScored counts as the kickoff conversion. RL kickoffs typically
// resolve inside ~10s; outside that, the play has settled into normal
// possession and "kickoff conversion" stops being a useful framing.
const kickoffWindow = 10 * time.Second

// matchSummaryEndedTimeout is the outer fallback: if PodiumStart never
// arrives (back-to-menu, network drop), flush the summary anyway after
// this long so subscribers don't wait forever. MatchDestroyed still
// short-circuits.
const matchSummaryEndedTimeout = 10 * time.Second

// matchSummaryPodiumSettle is the inner window we wait at PodiumStart
// for the late MVP Statfeed. MVP is part of the podium scene, so it
// arrives at or after PodiumStart — never before. 3s is generous
// enough for the slowest MVP statfeed observed. Tests override via
// SetSummaryTimings to keep the suite fast.
const matchSummaryPodiumSettle = 3 * time.Second

func NewSynthesizer(bus Broadcaster, roster *RosterTracker, correlation *CorrelationBuffer, ticks *TickStore) *Synthesizer {
	return &Synthesizer{
		bus:                 bus,
		roster:              roster,
		correlation:         correlation,
		ticks:               ticks,
		flipResetArmed:      make(map[string]bool),
		realGoalsByID:       make(map[string]int),
		flipResetCountByID:  make(map[string]int),
		demoChainByID:       make(map[string][]demoChainEntry),
		summaryEndedTimeout: matchSummaryEndedTimeout,
		summaryPodiumSettle: matchSummaryPodiumSettle,
	}
}

// SetSummaryTimings overrides the match-summary fallback / settle
// windows. Production callers leave defaults; tests pass tiny durations
// to avoid waiting seconds for timers to fire.
func (s *Synthesizer) SetSummaryTimings(endedTimeout, podiumSettle time.Duration) {
	s.summaryMu.Lock()
	s.summaryEndedTimeout = endedTimeout
	s.summaryPodiumSettle = podiumSettle
	s.summaryMu.Unlock()
}

// AttachMatchState wires the unified gameplay-state machine so emitters
// that should only fire during real gameplay can gate on its current
// phase, and diff emitters can skip replays / post-match phases (RL
// keeps streaming UpdateStates with stale boost / possession / score
// data into both, and we don't want to publish synthetic events for
// that phantom activity). Call before Run.
func (s *Synthesizer) AttachMatchState(m *MatchState) { s.matchState = m }

// AttachDiscoveryStore wires the persistent unknown-Statfeed registry.
// Optional — without it, _UnknownStatfeed still publishes but no entry
// is persisted to disk and the /api/statfeed-discoveries endpoint
// returns an empty list.
func (s *Synthesizer) AttachDiscoveryStore(store *StatfeedDiscoveryStore) {
	s.discoveries = store
}

// teamRef is the slice of UpdateState.Game.Teams[] we cache for downstream
// enrichment. Score is here for upcoming Phase-4 diff events; only Name
// and TeamNum are read by _MatchEnded.
type teamRef struct {
	TeamNum        int    `json:"teamNum"`
	Name           string `json:"name"`
	Score          int    `json:"score"`
	ColorPrimary   string `json:"colorPrimary,omitempty"`
	ColorSecondary string `json:"colorSecondary,omitempty"`
}

// Feed inspects each envelope and dispatches to the per-event synthesizer.
// Cheap when the event isn't one we synthesize (single name compare).
func (s *Synthesizer) Feed(raw []byte) {
	name := extractEventName(raw)
	switch name {
	case "UpdateState":
		s.onUpdateState(raw)
	case "StatfeedEvent":
		s.onStatfeedEvent(raw)
	case "CrossbarHit":
		s.onCrossbarHit(raw)
	case "MatchEnded":
		s.onMatchEnded(raw)
	case "GoalScored":
		s.onGoalScored(raw)
	case "MatchCreated":
		s.resetMatchMilestones()
	case "MatchDestroyed":
		// MatchDestroyed short-circuits the summary settle window so a
		// fast-quitting user still gets a _MatchSummary (with whatever
		// fields were collected). Also reset the per-match flags.
		s.flushMatchSummary("MatchDestroyed")
		s.resetMatchMilestones()
	case "RoundStarted":
		s.armKickoffWindow()
	case "PodiumStart":
		// MVP is part of the podium scene itself, so the MVP Statfeed
		// arrives at or after PodiumStart — never before. Restart the
		// settle window here so we wait for it instead of flushing the
		// summary immediately. MatchDestroyed still short-circuits.
		s.armPodiumSettle("PodiumStart")
	}
}

// resetMatchMilestones clears every per-match flag. Called on
// MatchCreated (new match starting) and MatchDestroyed (back to menu).
// Without this, a back-to-back rematch into the same lobby would
// silently skip _FirstBlood because firstBloodFired was still true.
func (s *Synthesizer) resetMatchMilestones() {
	// Per-match demo log clears with the milestone state so a back-to-
	// back rematch into the same lobby starts with an empty replay set.
	s.demolishMu.Lock()
	s.demolishLog = nil
	s.demolishMu.Unlock()
	// Drop the cached goal so the first GoalReplayStart of a new match
	// can't pick up the previous match's last goal.
	s.lastGoalMu.Lock()
	s.lastGoal = nil
	s.lastGoalMu.Unlock()
	// Drop any flip-reset arming so a stale flag from the previous
	// match can't tag the first goal of the next one.
	s.flipResetArmedMu.Lock()
	for k := range s.flipResetArmed {
		delete(s.flipResetArmed, k)
	}
	s.flipResetArmedMu.Unlock()
	// Reset per-player real-goal counters. Without this a player who
	// scored 2 honest goals last match would only need 1 more this
	// match to false-trigger _HatTrick.
	s.realGoalsMu.Lock()
	for k := range s.realGoalsByID {
		delete(s.realGoalsByID, k)
	}
	s.realGoalsMu.Unlock()
	// Reset per-player flip-reset counters so the next match starts
	// from 0 instead of carrying the previous match's count.
	s.flipResetCountMu.Lock()
	for k := range s.flipResetCountByID {
		delete(s.flipResetCountByID, k)
	}
	s.flipResetCountMu.Unlock()
	// Reset demo-chain history so a stale demo from the previous match
	// can't extend the next match's first demo into a phantom chain.
	s.demoChainMu.Lock()
	for k := range s.demoChainByID {
		delete(s.demoChainByID, k)
	}
	s.demoChainMu.Unlock()
	// Reset kickoff-conversion arming so a goal scored in the previous
	// match's settle window can't trigger _KickoffConverted in the next.
	s.kickoffMu.Lock()
	s.kickoffWindowDeadline = time.Time{}
	s.kickoffConvertedFired = false
	s.kickoffMu.Unlock()
}

// armKickoffWindow arms the per-round kickoff-conversion window on
// every RoundStarted. First _Shot or _GoalScored inside it fires
// _KickoffConverted; subsequent rounds re-arm by overwriting the
// deadline.
func (s *Synthesizer) armKickoffWindow() {
	s.kickoffMu.Lock()
	s.kickoffWindowDeadline = time.Now().Add(kickoffWindow)
	s.kickoffConvertedFired = false
	s.kickoffMu.Unlock()
}

// maybeEmitKickoffConverted fires _KickoffConverted on the first
// scoring action (_Shot or _GoalScored) inside the post-RoundStarted
// window. Once-per-round; subsequent shots/goals in the same round
// don't re-fire. The next RoundStarted re-arms.
func (s *Synthesizer) maybeEmitKickoffConverted(guid, source string, player *EnrichedPlayer, team int) {
	if player == nil {
		return
	}
	now := time.Now()
	s.kickoffMu.Lock()
	deadline := s.kickoffWindowDeadline
	if s.kickoffConvertedFired || deadline.IsZero() || now.After(deadline) {
		s.kickoffMu.Unlock()
		return
	}
	s.kickoffConvertedFired = true
	armed := deadline.Add(-kickoffWindow)
	s.kickoffMu.Unlock()

	elapsed := now.Sub(armed).Seconds()
	out := struct {
		Event                       string          `json:"Event"`
		MatchGUID                   string          `json:"matchGuid,omitempty"`
		Source                      string          `json:"source"`
		Player                      *EnrichedPlayer `json:"player"`
		TeamNum                     int             `json:"teamNum"`
		SecondsAfterRoundStart      float64         `json:"secondsAfterRoundStart"`
	}{
		Event:                  "_KickoffConverted",
		MatchGUID:              guid,
		Source:                 source,
		Player:                 player,
		TeamNum:                team,
		SecondsAfterRoundStart: elapsed,
	}
	if b, err := json.Marshal(out); err == nil {
		s.bus.Broadcast(Event{Raw: b})
	}
}

// tickSnapshot is the slice of UpdateState we cache for diff emitters.
// Only fields read by Phase-4 events are kept — the full envelope is
// big and we don't need most of it.
type tickSnapshot struct {
	matchGUID  string
	teams      []teamRef
	players    []tickPlayer
	ballTeam   int  // Game.Ball.TeamNum; 255 = untouched
	hasBall    bool // false when Game.Ball was absent
	overtime   bool
	bReplay    bool // Game.bReplay; rising edge marks a goal replay starting
}

// tickPlayer is the per-player slice of UpdateState we cache.
type tickPlayer struct {
	id          string
	name        string
	team        int
	score       int
	goals       int
	assists     int
	saves       int
	shots       int
	touches     int
	carTouches  int
	demos       int
	boost       *int // pointer because RL omits in non-spectator mode
	demolished  bool
	onGround    bool
	speed       *float64 // pointer: SPECTATOR-only field, omitted otherwise
	supersonic  bool
}

// updateStateFull mirrors the wire shape we need for Phase-4 diffs.
// Same case-tolerant pattern as the rest of the synthesizer: accept
// PascalCase or lowercase top-level keys.
type updateStateFull struct {
	MatchGUID    string                  `json:"MatchGuid"`
	MatchGUIDLow string                  `json:"matchguid"`
	Game         *updateStateGame        `json:"Game"`
	GameLow      *updateStateGame        `json:"game"`
	Players      []updateStateFullPlayer `json:"Players"`
	PlayersLow   []updateStateFullPlayer `json:"players"`
}

type updateStateGame struct {
	Teams     []wireTeam `json:"Teams"`
	Ball      *struct {
		TeamNum int `json:"TeamNum"`
	} `json:"Ball"`
	BOvertime bool `json:"bOvertime"`
	BReplay   bool `json:"bReplay"`
}

type updateStateFullPlayer struct {
	PrimaryID    string   `json:"PrimaryId"`
	Name         string   `json:"Name"`
	TeamNum      int      `json:"TeamNum"`
	Score        int      `json:"Score"`
	Goals        int      `json:"Goals"`
	Assists      int      `json:"Assists"`
	Saves        int      `json:"Saves"`
	Shots        int      `json:"Shots"`
	Touches      int      `json:"Touches"`
	CarTouches   int      `json:"CarTouches"`
	Demos        int      `json:"Demos"`
	Boost        *int     `json:"Boost"`
	Speed        *float64 `json:"Speed"`
	BDemolished  bool     `json:"bDemolished"`
	BOnGround    bool     `json:"bOnGround"`
	BSupersonic  bool     `json:"bSupersonic"`
}

// onUpdateState runs the diff emitters owned by the synthesizer:
// _PlayerJoined / _PlayerLeft, _PlayerScoreChanged, _BoostPickup,
// _BallPossessionChanged, _TeamScoreChanged, plus _OwnGoal. Reads the
// snapshot pair from the shared TickStore — the parse already
// happened in TickStore.Observe before this synth-bridged emit
// processor ran.
func (s *Synthesizer) onUpdateState(raw []byte) {
	curr := s.ticks.Latest()
	prev := s.ticks.Previous()
	if curr == nil {
		return
	}

	// _OwnGoal predates Phase 4 but is also a per-team-score-delta
	// emitter, so we keep it here. Skip the compare on the first tick
	// (no baseline).
	if prev != nil {
		s.detectOwnGoal(prev.teams, curr.teams, curr.matchGUID)
	}

	if prev == nil {
		// First tick: no diffs to compute.
		return
	}

	// Different-match guard: when the match guid changes, every
	// "diff" against the previous snapshot would be misleading. The
	// new tick is a fresh baseline.
	if prev.matchGUID != "" && curr.matchGUID != "" && prev.matchGUID != curr.matchGUID {
		return
	}

	// Replay-edge detection runs regardless — _GoalReplayContext is
	// the one event that's *supposed* to fire on entering replay.
	s.diffReplayEdge(prev, curr)

	// Roster + per-player score diffs run in any phase. Players can
	// join/leave or have stats reconciled in lobby, podium, etc.
	s.diffPlayers(prev, curr)

	// Play-state diffs only make sense during active gameplay. During
	// replay the ball/cars move on screen but nothing is actually
	// happening; on the post-match screen RL keeps streaming
	// UpdateStates with stale flags. Both would otherwise emit phantom
	// _BoostPickup / _BallPossessionChanged / _TeamScoreChanged /
	// _OvertimeStarted events.
	if s.matchState != nil {
		ph := s.matchState.Snapshot().Phase
		if ph != PhaseLive && ph != PhaseCountdown && ph != PhasePaused {
			return
		}
	}

	s.diffPlayersLive(prev, curr)
	s.diffTeamScores(prev, curr)
	s.diffBallPossession(prev, curr)
}

// diffReplayEdge emits _GoalReplayContext on a rising bReplay edge.
// Recent RL builds skip the discrete GoalReplayStart event entirely, so
// the bReplay flag on the per-tick Game snapshot is the only reliable
// "goal replay began" signal. We mirror the LifecycleTracker's edge
// detection here so the synthetic event fires on the same builds the
// rest of the toolkit already supports.
func (s *Synthesizer) diffReplayEdge(prev, curr *tickSnapshot) {
	if prev.bReplay || !curr.bReplay {
		return
	}
	s.lastGoalMu.Lock()
	cached := s.lastGoal
	s.lastGoalMu.Unlock()
	if cached == nil {
		return
	}
	// Mirror every field _GoalScored published, just renamed at the
	// wire level so subscribers can switch on Event. Copy by value so
	// downstream mutations of the cached envelope (none today, but
	// defensive) don't reach back into the source.
	out := *cached
	out.Event = "_GoalReplayContext"
	out.MatchGUID = curr.matchGUID
	if b, err := json.Marshal(out); err == nil {
		s.bus.Broadcast(Event{Raw: b})
	}
}

// diffPlayers walks the previous + current player lists and emits
// _PlayerJoined / _PlayerLeft on roster identity changes,
// _PlayerScoreChanged on stat-field deltas, and _BoostPickup on a
// rising boost edge during live play.
// diffPlayers emits roster changes (_PlayerJoined / _PlayerLeft).
// Roster identity moves can land in any phase (a player can drop
// during a goal replay), so this isn't phase-gated. Per-stat and
// play-state diffs live in diffPlayersLive.
func (s *Synthesizer) diffPlayers(prev, curr *tickSnapshot) {
	prevByID := make(map[string]*tickPlayer, len(prev.players))
	for i := range prev.players {
		p := &prev.players[i]
		if p.id != "" {
			prevByID[p.id] = p
		}
	}
	currByID := make(map[string]*tickPlayer, len(curr.players))
	for i := range curr.players {
		p := &curr.players[i]
		if p.id != "" {
			currByID[p.id] = p
		}
	}

	// _PlayerJoined: ids in curr but not in prev.
	for id, p := range currByID {
		if _, was := prevByID[id]; was {
			continue
		}
		s.emitPlayerJoined(curr.matchGUID, p)
	}
	// _PlayerLeft: ids in prev but not in curr.
	for id, p := range prevByID {
		if _, still := currByID[id]; still {
			continue
		}
		s.emitPlayerLeft(prev.matchGUID, p)
	}
}

// diffPlayersLive emits the play-state player diffs that only make
// sense during active gameplay:
//   - _PlayerScoreChanged: RL keeps streaming stats during goal
//     replays; the goal-frame delta already fired live, so any
//     replay-tick deltas are spurious reconciliation noise.
//   - _BoostPickup: cars on screen during replay also "pick up boost"
//     cosmetically; we don't want that.
//   - flip-reset bookkeeping: used by goal modifiers that only fire
//     during live play.
func (s *Synthesizer) diffPlayersLive(prev, curr *tickSnapshot) {
	prevByID := make(map[string]*tickPlayer, len(prev.players))
	for i := range prev.players {
		p := &prev.players[i]
		if p.id != "" {
			prevByID[p.id] = p
		}
	}
	for i := range curr.players {
		c := &curr.players[i]
		if c.id == "" {
			continue
		}
		p, ok := prevByID[c.id]
		if !ok {
			continue
		}
		s.emitPlayerScoreChangedIfDelta(curr.matchGUID, p, c)
		s.emitBoostPickupIfRising(curr.matchGUID, p, c)
		if !p.onGround && c.onGround {
			s.flipResetArmedMu.Lock()
			delete(s.flipResetArmed, c.id)
			s.flipResetArmedMu.Unlock()
		}
	}
}

// emitPlayerJoined publishes a _PlayerJoined envelope. The current
// gameplay phase is stamped on the wire so consumers can distinguish
// lobby joins from mid-match joins without their own phase
// subscription. Empty when no LifecycleTracker is attached.
func (s *Synthesizer) emitPlayerJoined(guid string, p *tickPlayer) {
	if p == nil || p.id == "" {
		return
	}
	enriched := &EnrichedPlayer{
		ID:       p.id,
		Name:     p.name,
		Team:     p.team,
		Platform: platformFromID(p.id),
		IsBot:    isBotId(p.id),
	}
	out := struct {
		Event     string          `json:"Event"`
		MatchGUID string          `json:"matchGuid,omitempty"`
		Player    *EnrichedPlayer `json:"player"`
		Phase     string          `json:"phase,omitempty"`
	}{
		Event:     "_PlayerJoined",
		MatchGUID: guid,
		Player:    enriched,
		Phase:     s.currentPhaseString(),
	}
	if b, err := json.Marshal(out); err == nil {
		s.bus.Broadcast(Event{Raw: b})
	}
}

func (s *Synthesizer) emitPlayerLeft(guid string, p *tickPlayer) {
	if p == nil || p.id == "" {
		return
	}
	enriched := &EnrichedPlayer{
		ID:       p.id,
		Name:     p.name,
		Team:     p.team,
		Platform: platformFromID(p.id),
		IsBot:    isBotId(p.id),
	}
	out := struct {
		Event     string          `json:"Event"`
		MatchGUID string          `json:"matchGuid,omitempty"`
		Player    *EnrichedPlayer `json:"player"`
		Phase     string          `json:"phase,omitempty"`
	}{
		Event:     "_PlayerLeft",
		MatchGUID: guid,
		Player:    enriched,
		Phase:     s.currentPhaseString(),
	}
	if b, err := json.Marshal(out); err == nil {
		s.bus.Broadcast(Event{Raw: b})
	}
}

// currentPhaseString returns the current MatchState phase as a
// wire-friendly string, or "" when no MatchState is attached.
func (s *Synthesizer) currentPhaseString() string {
	if s.matchState == nil {
		return ""
	}
	return string(s.matchState.Snapshot().Phase)
}

// emitPlayerScoreChangedIfDelta compares the seven non-spectator stat
// fields. Only sends a delta map for fields that actually moved, so
// subscribers can branch on (e.g.) `delta.demos > 0` without checking
// for the field's existence.
func (s *Synthesizer) emitPlayerScoreChangedIfDelta(guid string, prev, curr *tickPlayer) {
	delta := map[string]int{}
	if curr.score != prev.score {
		delta["score"] = curr.score - prev.score
	}
	if curr.goals != prev.goals {
		delta["goals"] = curr.goals - prev.goals
	}
	if curr.assists != prev.assists {
		delta["assists"] = curr.assists - prev.assists
	}
	if curr.saves != prev.saves {
		delta["saves"] = curr.saves - prev.saves
	}
	if curr.shots != prev.shots {
		delta["shots"] = curr.shots - prev.shots
	}
	if curr.touches != prev.touches {
		delta["touches"] = curr.touches - prev.touches
	}
	if curr.demos != prev.demos {
		delta["demos"] = curr.demos - prev.demos
	}
	if len(delta) == 0 {
		return
	}
	enriched := &EnrichedPlayer{
		ID:       curr.id,
		Name:     curr.name,
		Team:     curr.team,
		Platform: platformFromID(curr.id),
		IsBot:    isBotId(curr.id),
	}
	out := struct {
		Event     string          `json:"Event"`
		MatchGUID string          `json:"matchGuid,omitempty"`
		Player    *EnrichedPlayer `json:"player"`
		Delta     map[string]int  `json:"delta"`
	}{
		Event:     "_PlayerScoreChanged",
		MatchGUID: guid,
		Player:    enriched,
		Delta:     delta,
	}
	if b, err := json.Marshal(out); err == nil {
		s.bus.Broadcast(Event{Raw: b})
	}
}

// emitBoostPickupIfRising fires when the player's Boost increased
// (i.e., they picked up a pad or ran over a big-boost icon). Suppress
// the post-respawn case (demolished → not demolished) — that's a boost
// reset, not a pickup. Also suppress the first observation (no
// baseline), which happens when prev.boost is nil (non-spectator
// blackout) — we have no idea whether the value moved.
func (s *Synthesizer) emitBoostPickupIfRising(guid string, prev, curr *tickPlayer) {
	if prev.boost == nil || curr.boost == nil {
		return
	}
	if *curr.boost <= *prev.boost {
		return
	}
	if prev.demolished && !curr.demolished {
		// Respawn boost-reset, not a pickup.
		return
	}
	enriched := &EnrichedPlayer{
		ID:       curr.id,
		Name:     curr.name,
		Team:     curr.team,
		Platform: platformFromID(curr.id),
		IsBot:    isBotId(curr.id),
	}
	out := struct {
		Event       string          `json:"Event"`
		MatchGUID   string          `json:"matchGuid,omitempty"`
		Player      *EnrichedPlayer `json:"player"`
		BoostBefore int             `json:"boostBefore"`
		BoostAfter  int             `json:"boostAfter"`
		Delta       int             `json:"delta"`
	}{
		Event:       "_BoostPickup",
		MatchGUID:   guid,
		Player:      enriched,
		BoostBefore: *prev.boost,
		BoostAfter:  *curr.boost,
		Delta:       *curr.boost - *prev.boost,
	}
	if b, err := json.Marshal(out); err == nil {
		s.bus.Broadcast(Event{Raw: b})
	}
}

// diffTeamScores fires _TeamScoreChanged when any team's Score moves.
// Distinct from _OwnGoal: this fires for every score delta, including
// regular goals.
func (s *Synthesizer) diffTeamScores(prev, curr *tickSnapshot) {
	prevByNum := make(map[int]int, len(prev.teams))
	for _, t := range prev.teams {
		prevByNum[t.TeamNum] = t.Score
	}
	for _, t := range curr.teams {
		old, ok := prevByNum[t.TeamNum]
		if !ok || t.Score == old {
			continue
		}
		out := struct {
			Event     string `json:"Event"`
			MatchGUID string `json:"matchGuid,omitempty"`
			TeamNum   int    `json:"teamNum"`
			TeamName  string `json:"teamName,omitempty"`
			Before    int    `json:"before"`
			After     int    `json:"after"`
			Delta     int    `json:"delta"`
		}{
			Event:     "_TeamScoreChanged",
			MatchGUID: curr.matchGUID,
			TeamNum:   t.TeamNum,
			TeamName:  t.Name,
			Before:    old,
			After:     t.Score,
			Delta:     t.Score - old,
		}
		if b, err := json.Marshal(out); err == nil {
			s.bus.Broadcast(Event{Raw: b})
		}
	}
}

// diffBallPossession fires _BallPossessionChanged when the ball's
// TeamNum field changes. Normalize 255 (RL's "untouched" sentinel) to
// null in the JSON via *int.
func (s *Synthesizer) diffBallPossession(prev, curr *tickSnapshot) {
	if !prev.hasBall || !curr.hasBall {
		return
	}
	if prev.ballTeam == curr.ballTeam {
		return
	}
	toNullable := func(team int) *int {
		if team == 255 {
			return nil
		}
		t := team
		return &t
	}
	out := struct {
		Event       string                   `json:"Event"`
		MatchGUID   string                   `json:"matchGuid,omitempty"`
		Before      *int                     `json:"before"`
		After       *int                     `json:"after"`
		TriggeredBy *enrichedCorrelatedTouch `json:"triggeredBy,omitempty"`
	}{
		Event:       "_BallPossessionChanged",
		MatchGUID:   curr.matchGUID,
		Before:      toNullable(prev.ballTeam),
		After:       toNullable(curr.ballTeam),
		TriggeredBy: s.recentTouch(3),
	}
	if b, err := json.Marshal(out); err == nil {
		s.bus.Broadcast(Event{Raw: b})
	}
}

// detectOwnGoal compares two Teams[] snapshots and emits _OwnGoal when
// a team's score went up by 1 and the most recent ball touch was by an
// opposing-team player. Gated on the phase machine when attached so a
// forfeit/mercy-rule score-up doesn't false-positive.
func (s *Synthesizer) detectOwnGoal(prev, curr []teamRef, guid string) {
	// Build a quick lookup from TeamNum → previous score.
	prevByNum := make(map[int]int, len(prev))
	for _, p := range prev {
		prevByNum[p.TeamNum] = p.Score
	}

	for _, t := range curr {
		oldScore, ok := prevByNum[t.TeamNum]
		if !ok {
			continue
		}
		delta := t.Score - oldScore
		if delta != 1 {
			continue
		}
		// Phase gate: only fire during real gameplay. PhaseLive/Replay
		// covers active play; PhaseLobby/None/Countdown/Podium do not.
		if s.matchState != nil {
			ph := s.matchState.Snapshot().Phase
			if ph != PhaseLive && ph != PhaseReplay {
				continue
			}
		}

		// Find the most recent ball touch (within 5 events, ~1 tick).
		var touchPlayer *EnrichedPlayer
		for _, p := range s.correlation.Recent("BallHit", 5) {
			if rec, ok := p.(*ballHitRecord); ok && rec != nil && rec.Player != nil {
				touchPlayer = rec.Player
				break
			}
		}
		if touchPlayer == nil {
			continue
		}
		// Own-goal signature: deflector's team is NOT the team that
		// scored.
		if touchPlayer.Team == t.TeamNum {
			continue
		}

		// Optional: attach a recently-emitted _GoalScored if one fired
		// in the same window — gives subscribers the full goal payload
		// alongside the detection.
		var correlatedGoal *goalRecord
		for _, p := range s.correlation.Recent("_GoalScored", 5) {
			if g, ok := p.(*goalRecord); ok {
				correlatedGoal = g
				break
			}
		}

		out := enrichedOwnGoal{
			Event:         "_OwnGoal",
			MatchGUID:     guid,
			Deflector:     touchPlayer,
			ScoringTeam:   t.TeamNum,
			ConcedingTeam: touchPlayer.Team,
			ScoreAfter: ownGoalScoreAfter{
				Blue:   teamScore(curr, 0),
				Orange: teamScore(curr, 1),
			},
		}
		if correlatedGoal != nil && correlatedGoal.Scorer != nil {
			out.CorrelatedGoalScorer = correlatedGoal.Scorer
		}
		b, err := json.Marshal(out)
		if err != nil {
			continue
		}
		s.bus.Broadcast(Event{Raw: b})
	}
}

type enrichedOwnGoal struct {
	Event                string            `json:"Event"`
	MatchGUID            string            `json:"matchGuid,omitempty"`
	Deflector            *EnrichedPlayer   `json:"deflector,omitempty"`
	ScoringTeam          int               `json:"scoringTeam"`
	ConcedingTeam        int               `json:"concedingTeam"`
	ScoreAfter           ownGoalScoreAfter `json:"scoreAfter"`
	CorrelatedGoalScorer *EnrichedPlayer   `json:"correlatedGoalScorer,omitempty"`
}

type ownGoalScoreAfter struct {
	Blue   int `json:"blue"`
	Orange int `json:"orange"`
}

// teamScore reads the Score for a TeamNum out of a Teams[] snapshot,
// returning 0 if the team isn't present.
func teamScore(teams []teamRef, num int) int {
	for _, t := range teams {
		if t.TeamNum == num {
			return t.Score
		}
	}
	return 0
}

type gameTeams struct {
	Teams []wireTeam `json:"Teams"`
}

type wireTeam struct {
	TeamNum        int    `json:"TeamNum"`
	Name           string `json:"Name"`
	Score          int    `json:"Score"`
	ColorPrimary   string `json:"ColorPrimary"`
	ColorSecondary string `json:"ColorSecondary"`
}

// lookupTickScalars returns Speed and bSupersonic for the player with
// the given canonical id, from the most recent cached UpdateState. Speed
// is nil when no tick is cached, the player isn't in the tick, or the
// field was omitted (non-spectator). The boolean is meaningful only
// when the speed pointer is non-nil — see the supersonic-stamping
// guard in emitPlayerDemolished.
func (s *Synthesizer) lookupTickScalars(id string) (*float64, bool) {
	return s.ticks.PlayerScalars(id)
}

// teamByNum returns a copy of the cached team with the given TeamNum,
// or nil if no UpdateState has populated the cache or the team isn't
// present.
func (s *Synthesizer) teamByNum(num int) *teamRef {
	return s.ticks.TeamByNum(num)
}

// statfeedEnvelope mirrors the wire shape of a StatfeedEvent. RL ships
// either PascalCase or all-lowercase keys depending on build, so we
// accept both via the case-tolerant inner Data unwrap (envelopeData below).
type statfeedEnvelope struct {
	Data    string `json:"Data"`
	DataLow string `json:"data"`
}

type statfeedData struct {
	MatchGUID       string       `json:"MatchGuid"`
	MatchGUIDLow    string       `json:"matchguid"`
	EventName       string       `json:"EventName"`
	EventNameLow    string       `json:"eventname"`
	Type            string       `json:"Type"`
	TypeLow         string       `json:"type"`
	MainTarget      *ShortcutRef `json:"MainTarget"`
	MainTargetLow   *ShortcutRef `json:"maintarget"`
	SecondaryTarget *ShortcutRef `json:"SecondaryTarget"`
	SecondTargetLow *ShortcutRef `json:"secondarytarget"`
}

// enrichedStatfeed is the payload shape of _StatfeedEvent. Field names
// mirror the JS-side typed event so SDK consumers can switch without
// reshaping the data.
type enrichedStatfeed struct {
	Event           string          `json:"Event"`
	MatchGUID       string          `json:"matchGuid,omitempty"`
	EventName       string          `json:"eventName"`
	Type            string          `json:"type,omitempty"`
	MainTarget      *EnrichedPlayer `json:"mainTarget,omitempty"`
	SecondaryTarget *EnrichedPlayer `json:"secondaryTarget,omitempty"`
}

func (s *Synthesizer) onStatfeedEvent(raw []byte) {
	var env statfeedEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return
	}
	inner := env.Data
	if inner == "" {
		inner = env.DataLow
	}
	if inner == "" {
		return
	}
	var d statfeedData
	if err := json.Unmarshal([]byte(inner), &d); err != nil {
		return
	}

	guid := d.MatchGUID
	if guid == "" {
		guid = d.MatchGUIDLow
	}
	eventName := d.EventName
	if eventName == "" {
		eventName = d.EventNameLow
	}
	typeStr := d.Type
	if typeStr == "" {
		typeStr = d.TypeLow
	}
	main := d.MainTarget
	if main == nil {
		main = d.MainTargetLow
	}
	secondary := d.SecondaryTarget
	if secondary == nil {
		secondary = d.SecondTargetLow
	}

	out := enrichedStatfeed{
		Event:     "_StatfeedEvent",
		MatchGUID: guid,
		EventName: eventName,
		Type:      typeStr,
	}
	if main != nil {
		out.MainTarget = s.roster.ResolveByShortcut(*main)
	}
	if secondary != nil {
		out.SecondaryTarget = s.roster.ResolveByShortcut(*secondary)
	}

	// Record into the correlation buffer so _GoalScored / Phase-3 events
	// can look back for same-frame modifiers. Stash the wire ref for
	// shortcut-matching plus the resolved enrichment for forward use.
	s.correlation.Record("StatfeedEvent", &statfeedRecord{
		EventName: eventName,
		MainRef:   main,
		Resolved:  out.MainTarget,
	})

	b, err := json.Marshal(out)
	if err != nil {
		return
	}
	s.bus.Broadcast(Event{Raw: b})

	// Phase-3 dedicated events. Each known Statfeed variant gets its
	// own _-prefixed envelope with variant-specific fields (correlations,
	// counts) on top of the resolved targets. The generic _StatfeedEvent
	// above keeps firing as the catch-all.
	s.emitStatfeedVariant(eventName, guid, out.MainTarget, out.SecondaryTarget)

	// MVP arrives at or after PodiumStart. If _MatchSummary's settle
	// window is still open we attach it there (best-effort, gives a
	// single combined payload for plugins that only care about
	// summaries). Either way we also publish a dedicated _MatchMVP
	// event so MVP gets delivered even when RL ships it well after
	// _MatchSummary has flushed (observed: several seconds late).
	if eventName == "MVP" && out.MainTarget != nil {
		s.summaryMu.Lock()
		summaryGUID := s.summaryGUID
		winner := s.summaryWinner
		flushed := s.summaryFlushed
		matchEndedAt := s.summaryMatchEndedAt
		if s.summaryPending {
			s.summaryMVP = out.MainTarget
		}
		s.summaryMu.Unlock()

		mvpGUID := summaryGUID
		if mvpGUID == "" {
			mvpGUID = guid
		}
		var secondsAfter *float64
		if !matchEndedAt.IsZero() {
			d := time.Since(matchEndedAt).Seconds()
			secondsAfter = &d
		}
		mvpOut := struct {
			Event                string          `json:"Event"`
			MatchGUID            string          `json:"matchGuid,omitempty"`
			MVP                  *EnrichedPlayer `json:"mvp"`
			WinnerTeamNum        *int            `json:"winnerTeamNum,omitempty"`
			ArrivedAfterSummary  bool            `json:"arrivedAfterSummary"`
			SecondsAfterMatchEnd *float64        `json:"secondsAfterMatchEnd,omitempty"`
		}{
			Event:                "_MatchMVP",
			MatchGUID:            mvpGUID,
			MVP:                  out.MainTarget,
			WinnerTeamNum:        winner,
			ArrivedAfterSummary:  flushed,
			SecondsAfterMatchEnd: secondsAfter,
		}
		if b, err := json.Marshal(mvpOut); err == nil {
			s.bus.Broadcast(Event{Raw: b})
		}
	}
}

// emitStatfeedVariant fans the Statfeed variant out to its dedicated
// _-prefixed event. Untracked variants (Goal, Win, MVP, Playmaker,
// Savior, LowFive, HighFive — see the plan's Phase 3.3) are silently
// skipped; the generic _StatfeedEvent already covered them. Names not
// in the verified registry produce _UnknownStatfeed for discoverability.
func (s *Synthesizer) emitStatfeedVariant(eventName, guid string, main, secondary *EnrichedPlayer) {
	if _, known := verifiedStatfeedNames[eventName]; !known {
		s.emitUnknownStatfeed(eventName, guid, main, secondary)
	}
	switch eventName {
	case "Demolish":
		s.emitPlayerDemolished(guid, main, secondary)
	case "FlipReset":
		s.emitFlipReset(guid, main)
	case "HatTrick":
		s.emitHatTrick(guid, main)
	case "Save":
		s.emitSave(guid, main, "_Save")
	case "EpicSave":
		s.emitSave(guid, main, "_EpicSave")
	case "Shot":
		s.emitShot(guid, main)
	case "Assist":
		s.emitAssist(guid, main)
	case "Center":
		s.emitTouchVariant(guid, main, "_Center")
	case "Clear":
		s.emitTouchVariant(guid, main, "_Clear")
	case "BicycleHit":
		s.emitTouchVariant(guid, main, "_BicycleHit")
	}
}

// emitUnknownStatfeed publishes _UnknownStatfeed and bumps the
// persistent discoveries store (if attached). The "first observation"
// case logs once per name so a maintainer notices.
func (s *Synthesizer) emitUnknownStatfeed(eventName, guid string, main, secondary *EnrichedPlayer) {
	out := struct {
		Event           string          `json:"Event"`
		MatchGUID       string          `json:"matchGuid,omitempty"`
		EventName       string          `json:"eventName"`
		MainTarget      *EnrichedPlayer `json:"mainTarget,omitempty"`
		SecondaryTarget *EnrichedPlayer `json:"secondaryTarget,omitempty"`
	}{
		Event:           "_UnknownStatfeed",
		MatchGUID:       guid,
		EventName:       eventName,
		MainTarget:      main,
		SecondaryTarget: secondary,
	}
	if b, err := json.Marshal(out); err == nil {
		s.bus.Broadcast(Event{Raw: b})
	}
	if s.discoveries != nil {
		s.discoveries.Record(eventName)
	}
}

// emitSimple publishes a minimal envelope with just the resolved
// MainTarget. Used by stat events with no correlation logic.
func (s *Synthesizer) emitSimple(eventName, guid string, main *EnrichedPlayer) {
	if main == nil {
		return
	}
	b, err := json.Marshal(struct {
		Event      string          `json:"Event"`
		MatchGUID  string          `json:"matchGuid,omitempty"`
		MainTarget *EnrichedPlayer `json:"mainTarget"`
	}{
		Event:      eventName,
		MatchGUID:  guid,
		MainTarget: main,
	})
	if err != nil {
		return
	}
	s.bus.Broadcast(Event{Raw: b})
}

func (s *Synthesizer) emitFlipReset(guid string, main *EnrichedPlayer) {
	if main == nil {
		return
	}
	// Arm the flip-reset-goal modifier for this player. Cleared when
	// they next touch ground (in diffPlayers) or score (consumed in
	// onGoalScored). Multiple resets in the same airborne run all map
	// to the same armed state; we don't count them.
	if main.ID != "" {
		s.flipResetArmedMu.Lock()
		s.flipResetArmed[main.ID] = true
		s.flipResetArmedMu.Unlock()
	}
	// Per-match counter — bump on every reset (no de-dup; consecutive
	// resets in one airborne run all increment). Stamped on the wire
	// so consumers don't keep their own state.
	count := 0
	if main.ID != "" {
		s.flipResetCountMu.Lock()
		s.flipResetCountByID[main.ID]++
		count = s.flipResetCountByID[main.ID]
		s.flipResetCountMu.Unlock()
	}
	out := struct {
		Event                 string          `json:"Event"`
		MatchGUID             string          `json:"matchGuid,omitempty"`
		MainTarget            *EnrichedPlayer `json:"mainTarget"`
		FlipResetsThisMatch   int             `json:"flipResetsThisMatch"`
	}{
		Event:               "_FlipReset",
		MatchGUID:           guid,
		MainTarget:          main,
		FlipResetsThisMatch: count,
	}
	if b, err := json.Marshal(out); err == nil {
		s.bus.Broadcast(Event{Raw: b})
	}
}

// _PlayerDemolished carries both attacker (MainTarget) and victim
// (SecondaryTarget). isSelfDemo / isTeamDemo derived from team comparison.
// attackerSpeed and attackerWasSupersonic are pulled from the most
// recent UpdateState's per-player slice when available (SPECTATOR-only,
// so non-spectator clients see them omitted).
func (s *Synthesizer) emitPlayerDemolished(guid string, attacker, victim *EnrichedPlayer) {
	if attacker == nil || victim == nil {
		// Without one of the two targets the event is meaningless;
		// the catch-all _StatfeedEvent already covered it.
		return
	}
	attackerSpeed, attackerSupersonic := s.lookupTickScalars(attacker.ID)
	out := struct {
		Event                 string          `json:"Event"`
		MatchGUID             string          `json:"matchGuid,omitempty"`
		Attacker              *EnrichedPlayer `json:"attacker"`
		Victim                *EnrichedPlayer `json:"victim"`
		IsSelfDemo            bool            `json:"isSelfDemo,omitempty"`
		IsTeamDemo            bool            `json:"isTeamDemo,omitempty"`
		AttackerSpeed         *float64        `json:"attackerSpeed,omitempty"`
		AttackerWasSupersonic *bool           `json:"attackerWasSupersonic,omitempty"`
	}{
		Event:         "_PlayerDemolished",
		MatchGUID:     guid,
		Attacker:      attacker,
		Victim:        victim,
		AttackerSpeed: attackerSpeed,
	}
	if attackerSpeed != nil {
		// Only stamp the supersonic flag when we have a tick row for the
		// attacker — otherwise we'd be claiming `false` from missing data.
		ss := attackerSupersonic
		out.AttackerWasSupersonic = &ss
	}
	if attacker.ID != "" && attacker.ID == victim.ID {
		out.IsSelfDemo = true
	} else if attacker.Team == victim.Team {
		out.IsTeamDemo = true
	}
	b, err := json.Marshal(out)
	if err != nil {
		return
	}
	s.bus.Broadcast(Event{Raw: b})
	s.demolishMu.Lock()
	s.demolishLog = append(s.demolishLog, b)
	s.demolishMu.Unlock()
	// Demo-chain detection: ≥2 demos by the same attacker within the
	// rolling window. Self-demos and team-demos still count as actions
	// but skip chain reporting (a chain over your own teammate isn't a
	// hype moment); we keep them out of the per-attacker history too so
	// they don't extend chains over real demos.
	if !out.IsSelfDemo && !out.IsTeamDemo && attacker.ID != "" {
		s.maybeEmitDemoChain(guid, attacker, victim)
	}
}

// maybeEmitDemoChain trims the attacker's history to the chain window
// and fires _DemoChain when count ≥ 2. Each subsequent demo inside the
// window re-fires with the updated count and victim list.
func (s *Synthesizer) maybeEmitDemoChain(guid string, attacker, victim *EnrichedPlayer) {
	now := time.Now()
	cutoff := now.Add(-demoChainWindow)
	s.demoChainMu.Lock()
	hist := s.demoChainByID[attacker.ID]
	// Drop entries outside the window.
	trimmed := hist[:0]
	for _, e := range hist {
		if e.at.After(cutoff) {
			trimmed = append(trimmed, e)
		}
	}
	trimmed = append(trimmed, demoChainEntry{at: now, victim: victim})
	s.demoChainByID[attacker.ID] = trimmed
	count := len(trimmed)
	victims := make([]*EnrichedPlayer, 0, count)
	for _, e := range trimmed {
		victims = append(victims, e.victim)
	}
	windowStart := trimmed[0].at
	s.demoChainMu.Unlock()

	if count < 2 {
		return
	}
	windowSeconds := now.Sub(windowStart).Seconds()
	out := struct {
		Event         string            `json:"Event"`
		MatchGUID     string            `json:"matchGuid,omitempty"`
		Attacker      *EnrichedPlayer   `json:"attacker"`
		Victims       []*EnrichedPlayer `json:"victims"`
		Count         int               `json:"count"`
		WindowSeconds float64           `json:"windowSeconds"`
	}{
		Event:         "_DemoChain",
		MatchGUID:     guid,
		Attacker:      attacker,
		Victims:       victims,
		Count:         count,
		WindowSeconds: windowSeconds,
	}
	if b, err := json.Marshal(out); err == nil {
		s.bus.Broadcast(Event{Raw: b})
	}
}

// DemolishLog returns a snapshot of every _PlayerDemolished envelope
// published since the current match started. Used by the SSE handler
// to replay the per-match demo history to a freshly-connected client
// so per-match counters survive page refreshes without each plugin
// implementing its own persistence. Returns nil when no demos have
// been logged yet.
func (s *Synthesizer) DemolishLog() [][]byte {
	s.demolishMu.Lock()
	defer s.demolishMu.Unlock()
	if len(s.demolishLog) == 0 {
		return nil
	}
	out := make([][]byte, len(s.demolishLog))
	copy(out, s.demolishLog)
	return out
}

// _HatTrick fires when the scorer's Goals count hits 3. RL's HatTrick
// Statfeed counts own goals toward the threshold; we don't, so we
// suppress the event when the player's tracked real-goal count is
// below 3. Goal count comes from the per-match real-goal counter.
func (s *Synthesizer) emitHatTrick(guid string, main *EnrichedPlayer) {
	if main == nil {
		return
	}
	s.realGoalsMu.Lock()
	real := s.realGoalsByID[main.ID]
	s.realGoalsMu.Unlock()
	if real < 3 {
		return
	}
	out := struct {
		Event           string          `json:"Event"`
		MatchGUID       string          `json:"matchGuid,omitempty"`
		MainTarget      *EnrichedPlayer `json:"mainTarget"`
		GoalsThisMatch  int             `json:"goalsThisMatch"`
	}{
		Event:          "_HatTrick",
		MatchGUID:      guid,
		MainTarget:     main,
		GoalsThisMatch: real,
	}
	b, err := json.Marshal(out)
	if err != nil {
		return
	}
	s.bus.Broadcast(Event{Raw: b})
}

// _Save / _EpicSave share their shape; differ only in event name. The
// plan's correlatedShot lookup ships in a follow-up — for now we expose
// the resolved saver and let consumers correlate on their own.
func (s *Synthesizer) emitSave(guid string, main *EnrichedPlayer, eventName string) {
	if main == nil {
		return
	}
	// Look back for a recent Shot statfeed by an opposing-team player.
	// Saves can land several events after the shot (intervening BallHit /
	// other StatfeedEvents), so use the full buffer window.
	var correlatedShot *EnrichedPlayer
	for _, p := range s.correlation.Recent("StatfeedEvent", 15) {
		rec, ok := p.(*statfeedRecord)
		if !ok {
			continue
		}
		if rec.EventName != "Shot" {
			continue
		}
		if rec.Resolved == nil || rec.Resolved.Team == main.Team {
			continue
		}
		correlatedShot = rec.Resolved
		break
	}

	out := struct {
		Event          string          `json:"Event"`
		MatchGUID      string          `json:"matchGuid,omitempty"`
		MainTarget     *EnrichedPlayer `json:"mainTarget"`
		CorrelatedShot *EnrichedPlayer `json:"correlatedShot,omitempty"`
	}{
		Event:          eventName,
		MatchGUID:      guid,
		MainTarget:     main,
		CorrelatedShot: correlatedShot,
	}
	b, err := json.Marshal(out)
	if err != nil {
		return
	}
	s.bus.Broadcast(Event{Raw: b})
}

// _Shot. We attempt to attach the same-frame BallHit (the touch that
// produced the shot) when one is in the buffer.
func (s *Synthesizer) emitShot(guid string, main *EnrichedPlayer) {
	if main == nil {
		return
	}
	correlatedTouch := s.recentTouch(3)
	out := struct {
		Event           string                   `json:"Event"`
		MatchGUID       string                   `json:"matchGuid,omitempty"`
		MainTarget      *EnrichedPlayer          `json:"mainTarget"`
		CorrelatedTouch *enrichedCorrelatedTouch `json:"correlatedTouch,omitempty"`
	}{
		Event:           "_Shot",
		MatchGUID:       guid,
		MainTarget:      main,
		CorrelatedTouch: correlatedTouch,
	}
	defer s.maybeEmitKickoffConverted(guid, "Shot", main, main.Team)
	b, err := json.Marshal(out)
	if err != nil {
		return
	}
	s.bus.Broadcast(Event{Raw: b})
}

// _Assist. Look back for a same-frame _GoalScored — assists land on
// the same tick as the goal in most builds, but the plan calls this
// provisional because the window may need adjustment. We use 5 events.
func (s *Synthesizer) emitAssist(guid string, main *EnrichedPlayer) {
	if main == nil {
		return
	}
	var correlatedGoal *goalRecord
	for _, p := range s.correlation.Recent("_GoalScored", 5) {
		if g, ok := p.(*goalRecord); ok {
			correlatedGoal = g
			break
		}
	}
	type goalRef struct {
		Scorer        *EnrichedPlayer `json:"scorer,omitempty"`
		ScoringTeam   int             `json:"scoringTeam"`
		ConcedingTeam int             `json:"concedingTeam"`
	}
	var goalSummary *goalRef
	if correlatedGoal != nil {
		goalSummary = &goalRef{
			Scorer:        correlatedGoal.Scorer,
			ScoringTeam:   correlatedGoal.ScoringTeam,
			ConcedingTeam: correlatedGoal.ConcedingTeam,
		}
	}
	out := struct {
		Event          string          `json:"Event"`
		MatchGUID      string          `json:"matchGuid,omitempty"`
		MainTarget     *EnrichedPlayer `json:"mainTarget"`
		CorrelatedGoal *goalRef        `json:"correlatedGoal,omitempty"`
	}{
		Event:          "_Assist",
		MatchGUID:      guid,
		MainTarget:     main,
		CorrelatedGoal: goalSummary,
	}
	b, err := json.Marshal(out)
	if err != nil {
		return
	}
	s.bus.Broadcast(Event{Raw: b})
}

// emitTouchVariant covers _Center, _Clear, _BicycleHit — same shape:
// resolved MainTarget + a correlatedTouch pulled from the BallHit
// buffer (with pre/post hit speeds when present).
func (s *Synthesizer) emitTouchVariant(guid string, main *EnrichedPlayer, eventName string) {
	if main == nil {
		return
	}
	correlatedTouch := s.recentTouch(3)
	out := struct {
		Event           string                   `json:"Event"`
		MatchGUID       string                   `json:"matchGuid,omitempty"`
		MainTarget      *EnrichedPlayer          `json:"mainTarget"`
		CorrelatedTouch *enrichedCorrelatedTouch `json:"correlatedTouch,omitempty"`
	}{
		Event:           eventName,
		MatchGUID:       guid,
		MainTarget:      main,
		CorrelatedTouch: correlatedTouch,
	}
	b, err := json.Marshal(out)
	if err != nil {
		return
	}
	s.bus.Broadcast(Event{Raw: b})
}

// statfeedRecord is what the correlation buffer holds for each
// StatfeedEvent. Only the fields _GoalScored / Phase-3 emitters look at
// are kept — small footprint per entry.
type statfeedRecord struct {
	EventName string
	MainRef   *ShortcutRef
	Resolved  *EnrichedPlayer
}

// ballHitRecord is the correlation-buffer entry for a BallHit. Carries
// the resolved primary toucher plus the scalar speed fields from the
// envelope so downstream emitters (_Shot / _Center / _Clear / _BicycleHit /
// _BallPossessionChanged) can attach them without re-parsing.
type ballHitRecord struct {
	Player       *EnrichedPlayer
	PreHitSpeed  *float64
	PostHitSpeed *float64
}

// enrichedCorrelatedTouch is the wire shape for `correlatedTouch` on
// touch-variant events (_Shot / _Center / _Clear / _BicycleHit) and on
// _BallPossessionChanged.triggeredBy. Speeds are nullable since RL omits
// them on some BallHit envelopes.
type enrichedCorrelatedTouch struct {
	Player       *EnrichedPlayer `json:"player,omitempty"`
	PreHitSpeed  *float64        `json:"preHitSpeed,omitempty"`
	PostHitSpeed *float64        `json:"postHitSpeed,omitempty"`
}

// recentTouch returns the most recent BallHit from the correlation
// buffer as a wire-ready correlatedTouch envelope. Returns nil when no
// matching record exists or the player wasn't resolved.
func (s *Synthesizer) recentTouch(window int) *enrichedCorrelatedTouch {
	for _, p := range s.correlation.Recent("BallHit", window) {
		rec, ok := p.(*ballHitRecord)
		if !ok || rec == nil || rec.Player == nil {
			continue
		}
		return &enrichedCorrelatedTouch{
			Player:       rec.Player,
			PreHitSpeed:  rec.PreHitSpeed,
			PostHitSpeed: rec.PostHitSpeed,
		}
	}
	return nil
}

// unwrapInnerData pulls the inner Data string out of an envelope, accepting
// both PascalCase and lowercase top-level keys. Returns "" on missing or
// malformed envelope.
func unwrapInnerData(raw []byte) string {
	var env struct {
		Data    string `json:"Data"`
		DataLow string `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return ""
	}
	if env.Data != "" {
		return env.Data
	}
	return env.DataLow
}

// ballRef is the ball location/speed block found on BallHit and CrossbarHit.
type ballRef struct {
	PreHitSpeed  *float64 `json:"PreHitSpeed,omitempty"`
	PostHitSpeed *float64 `json:"PostHitSpeed,omitempty"`
	Location     *vec3    `json:"Location,omitempty"`
}

type vec3 struct {
	X float64 `json:"X"`
	Y float64 `json:"Y"`
	Z float64 `json:"Z"`
}

type ballHitData struct {
	MatchGUID    string         `json:"MatchGuid"`
	MatchGUIDLow string         `json:"matchguid"`
	Players      []ShortcutRef  `json:"Players"`
	PlayersLow   []ShortcutRef  `json:"players"`
	Ball         *ballHitInner  `json:"Ball"`
	BallLow      *ballHitInner  `json:"ball"`
}

type ballHitInner struct {
	PreHitSpeed  *float64 `json:"PreHitSpeed"`
	PostHitSpeed *float64 `json:"PostHitSpeed"`
	Location     *vec3    `json:"Location"`
}

type enrichedBallHit struct {
	Event        string            `json:"Event"`
	MatchGUID    string            `json:"matchGuid,omitempty"`
	Players      []*EnrichedPlayer `json:"players"`
	PreHitSpeed  *float64          `json:"preHitSpeed,omitempty"`
	PostHitSpeed *float64          `json:"postHitSpeed,omitempty"`
	Location     *vec3             `json:"location,omitempty"`
}

// crossbarHitData mirrors the wire shape of a CrossbarHit envelope.
// BallLastTouch is the player who last touched the ball before it hit
// the crossbar — typically the shooter.
type crossbarHitData struct {
	MatchGUID        string         `json:"MatchGuid"`
	MatchGUIDLow     string         `json:"matchguid"`
	BallSpeed        *float64       `json:"BallSpeed"`
	BallSpeedLow     *float64       `json:"ballspeed"`
	ImpactForce      *float64       `json:"ImpactForce"`
	ImpactForceLow   *float64       `json:"impactforce"`
	BallLocation     *vec3          `json:"BallLocation"`
	BallLocationLow  *vec3          `json:"balllocation"`
	BallLastTouch    *ballLastTouch `json:"BallLastTouch"`
	BallLastTouchLow *ballLastTouch `json:"balllasttouch"`
}

type ballLastTouch struct {
	Player    *ShortcutRef `json:"Player"`
	PlayerLow *ShortcutRef `json:"player"`
	Speed     *float64     `json:"Speed"`
	SpeedLow  *float64     `json:"speed"`
}

type enrichedBallLastTouch struct {
	Player *EnrichedPlayer `json:"player,omitempty"`
	Speed  *float64        `json:"speed,omitempty"`
}

type enrichedCrossbarHit struct {
	Event         string                 `json:"Event"`
	MatchGUID     string                 `json:"matchGuid,omitempty"`
	BallSpeed     *float64               `json:"ballSpeed,omitempty"`
	ImpactForce   *float64               `json:"impactForce,omitempty"`
	BallLocation  *vec3                  `json:"ballLocation,omitempty"`
	BallLastTouch *enrichedBallLastTouch `json:"ballLastTouch,omitempty"`
}

// crossbarDebounceWindow suppresses repeat _CrossbarHit emissions
// when RL fires a burst (ball rolling along the goal frame). 500ms is
// long enough to absorb the burst, short enough that two genuinely
// distinct hits in a single play still both fire.
const crossbarDebounceWindow = 500 * time.Millisecond

func (s *Synthesizer) onCrossbarHit(raw []byte) {
	// Phase gate: catalog says liveOnly but the dispatch wasn't
	// enforcing it. RL still fires CrossbarHit during goal replays
	// and the cinematic camera bouncing the ball off the frame
	// shouldn't count. bReplay on the cached UpdateState is the
	// canonical replay signal on this build (discrete
	// GoalReplayStart/End events are unreliable).
	inReplay := s.ticks.InReplay()
	if inReplay {
		return
	}

	// Debounce: drop repeats within the window. Updates the timestamp
	// even on drops so a long roll keeps suppressing.
	now := time.Now()
	s.crossbarMu.Lock()
	if !s.lastCrossbarHit.IsZero() && now.Sub(s.lastCrossbarHit) < crossbarDebounceWindow {
		s.lastCrossbarHit = now
		s.crossbarMu.Unlock()
		return
	}
	s.lastCrossbarHit = now
	s.crossbarMu.Unlock()

	inner := unwrapInnerData(raw)
	if inner == "" {
		return
	}
	var d crossbarHitData
	if err := json.Unmarshal([]byte(inner), &d); err != nil {
		return
	}

	guid := pickStr(d.MatchGUID, d.MatchGUIDLow)
	speed := pickFloat(d.BallSpeed, d.BallSpeedLow)
	force := pickFloat(d.ImpactForce, d.ImpactForceLow)
	loc := d.BallLocation
	if loc == nil {
		loc = d.BallLocationLow
	}
	lastTouch := d.BallLastTouch
	if lastTouch == nil {
		lastTouch = d.BallLastTouchLow
	}

	out := enrichedCrossbarHit{
		Event:        "_CrossbarHit",
		MatchGUID:    guid,
		BallSpeed:    speed,
		ImpactForce:  force,
		BallLocation: loc,
	}
	if lastTouch != nil {
		ref := lastTouch.Player
		if ref == nil {
			ref = lastTouch.PlayerLow
		}
		sp := pickFloat(lastTouch.Speed, lastTouch.SpeedLow)
		var enrichedRef *EnrichedPlayer
		if ref != nil {
			enrichedRef = s.roster.ResolveByShortcut(*ref)
		}
		if enrichedRef != nil || sp != nil {
			out.BallLastTouch = &enrichedBallLastTouch{
				Player: enrichedRef,
				Speed:  sp,
			}
		}
	}

	b, err := json.Marshal(out)
	if err != nil {
		return
	}
	s.bus.Broadcast(Event{Raw: b})
}

func pickStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func pickFloat(a, b *float64) *float64 {
	if a != nil {
		return a
	}
	return b
}

// matchEndedData mirrors the wire shape of MatchEnded.
type matchEndedData struct {
	MatchGUID       string `json:"MatchGuid"`
	MatchGUIDLow    string `json:"matchguid"`
	WinnerTeamNum   *int   `json:"WinnerTeamNum"`
	WinnerTeamLow   *int   `json:"winnerteamnum"`
}

type enrichedMatchEnded struct {
	Event         string  `json:"Event"`
	MatchGUID     string  `json:"matchGuid,omitempty"`
	WinnerTeamNum *int    `json:"winnerTeamNum,omitempty"`
	WinnerName    string  `json:"winnerName,omitempty"`
	ScoreBlue     *int    `json:"scoreBlue,omitempty"`
	ScoreOrange   *int    `json:"scoreOrange,omitempty"`
}

func (s *Synthesizer) onMatchEnded(raw []byte) {
	inner := unwrapInnerData(raw)
	if inner == "" {
		return
	}
	var d matchEndedData
	if err := json.Unmarshal([]byte(inner), &d); err != nil {
		return
	}
	guid := pickStr(d.MatchGUID, d.MatchGUIDLow)
	winner := d.WinnerTeamNum
	if winner == nil {
		winner = d.WinnerTeamLow
	}

	out := enrichedMatchEnded{
		Event:         "_MatchEnded",
		MatchGUID:     guid,
		WinnerTeamNum: winner,
	}

	// Resolve final scores from the cached Teams[] so subscribers don't
	// have to track UpdateState themselves.
	if blue := s.teamByNum(0); blue != nil {
		score := blue.Score
		out.ScoreBlue = &score
	}
	if orange := s.teamByNum(1); orange != nil {
		score := orange.Score
		out.ScoreOrange = &score
	}
	if winner != nil {
		if t := s.teamByNum(*winner); t != nil {
			out.WinnerName = t.Name
		}
	}

	b, err := json.Marshal(out)
	if err != nil {
		return
	}
	s.bus.Broadcast(Event{Raw: b})

	// Begin the _MatchSummary settle window. We cache the current
	// state (winner + final UpdateState snapshot) and start a 2s
	// timer that publishes the summary unless PodiumStart /
	// MatchDestroyed flushes earlier.
	s.beginMatchSummary(guid, winner)
}

// beginMatchSummary captures the final-tick state and arms an outer
// fallback timer in case PodiumStart never fires (back-to-menu, network
// drop). The real flush happens from armPodiumSettle (inner window for
// the late MVP Statfeed) or MatchDestroyed (immediate).
//
// If a summary is already pending (back-to-back MatchEnded —
// shouldn't happen in practice, but defensive), the existing one is
// cancelled and the new one takes over.
func (s *Synthesizer) beginMatchSummary(guid string, winner *int) {
	finalSnap := s.ticks.Latest()

	s.summaryMu.Lock()
	if s.summaryPending && s.summaryCancel != nil {
		close(s.summaryCancel)
	}
	s.summaryPending = true
	s.summaryFlushed = false
	s.summaryMatchEndedAt = time.Now()
	s.summaryGUID = guid
	s.summaryWinner = winner
	s.summaryFinalSnap = finalSnap
	s.summaryMVP = nil
	cancel := make(chan struct{})
	s.summaryCancel = cancel
	s.summaryMu.Unlock()

	endedTimeout := s.summaryEndedTimeout
	go func() {
		select {
		case <-cancel:
			// PodiumStart rearmed the timer, or MatchDestroyed
			// flushed early — either way we're done here.
		case <-time.After(endedTimeout):
			s.flushMatchSummary("endedTimeout")
		}
	}()
}

// armPodiumSettle restarts the settle window with a shorter post-podium
// timer that gives RL time to ship the MVP Statfeed (which arrives only
// at/after PodiumStart). On timeout, the summary flushes — with MVP if
// the statfeed arrived during the window, without if it didn't.
func (s *Synthesizer) armPodiumSettle(trigger string) {
	s.summaryMu.Lock()
	if !s.summaryPending {
		s.summaryMu.Unlock()
		return
	}
	if s.summaryCancel != nil {
		close(s.summaryCancel)
	}
	cancel := make(chan struct{})
	s.summaryCancel = cancel
	settle := s.summaryPodiumSettle
	s.summaryMu.Unlock()

	go func() {
		select {
		case <-cancel:
			// MatchDestroyed flushed early.
		case <-time.After(settle):
			s.flushMatchSummary(trigger)
		}
	}()
}

// flushMatchSummary publishes _MatchSummary using the captured state.
// Idempotent — if no summary is pending, it's a no-op. The trigger
// string lands on the envelope so subscribers can tell which path
// fired the summary.
func (s *Synthesizer) flushMatchSummary(trigger string) {
	s.summaryMu.Lock()
	if !s.summaryPending {
		s.summaryMu.Unlock()
		return
	}
	s.summaryPending = false
	s.summaryFlushed = true
	guid := s.summaryGUID
	winner := s.summaryWinner
	finalSnap := s.summaryFinalSnap
	mvp := s.summaryMVP
	cancel := s.summaryCancel
	s.summaryCancel = nil
	s.summaryMu.Unlock()

	if cancel != nil {
		// Wake the goroutine so it doesn't sit on the timer pointlessly.
		// A double-close would panic; cancel is set to nil under the
		// lock above, so subsequent calls can't reach this branch.
		select {
		case <-cancel:
			// already closed (settleTimeout path)
		default:
			close(cancel)
		}
	}

	// Build the summary payload. Winner name + scores from the
	// captured snapshot; players list with full per-player stats so
	// post-game UI can render leaderboards without UpdateState.
	type playerStat struct {
		Player  *EnrichedPlayer `json:"player"`
		Score   int             `json:"score"`
		Goals   int             `json:"goals"`
		Assists int             `json:"assists"`
		Saves   int             `json:"saves"`
		Shots   int             `json:"shots"`
		Demos   int             `json:"demos"`
	}
	var players []playerStat
	scoreBlue, scoreOrange := 0, 0
	winnerName := ""
	if finalSnap != nil {
		scoreBlue = teamScore(finalSnap.teams, 0)
		scoreOrange = teamScore(finalSnap.teams, 1)
		if winner != nil {
			for _, t := range finalSnap.teams {
				if t.TeamNum == *winner {
					winnerName = t.Name
					break
				}
			}
		}
		for _, p := range finalSnap.players {
			players = append(players, playerStat{
				Player: &EnrichedPlayer{
					ID:       p.id,
					Name:     p.name,
					Team:     p.team,
					Platform: platformFromID(p.id),
					IsBot:    isBotId(p.id),
				},
				Score:   p.score,
				Goals:   p.goals,
				Assists: p.assists,
				Saves:   p.saves,
				Shots:   p.shots,
				Demos:   p.demos,
			})
		}
	}

	out := struct {
		Event         string          `json:"Event"`
		MatchGUID     string          `json:"matchGuid,omitempty"`
		WinnerTeamNum *int            `json:"winnerTeamNum,omitempty"`
		WinnerName    string          `json:"winnerName,omitempty"`
		ScoreBlue     int             `json:"scoreBlue"`
		ScoreOrange   int             `json:"scoreOrange"`
		MVP           *EnrichedPlayer `json:"mvp"`
		Players       []playerStat    `json:"players,omitempty"`
		Trigger       string          `json:"trigger"`
	}{
		Event:         "_MatchSummary",
		MatchGUID:     guid,
		WinnerTeamNum: winner,
		WinnerName:    winnerName,
		ScoreBlue:     scoreBlue,
		ScoreOrange:   scoreOrange,
		MVP:           mvp,
		Players:       players,
		Trigger:       trigger,
	}
	if b, err := json.Marshal(out); err == nil {
		s.bus.Broadcast(Event{Raw: b})
	}
}

// goalScoredData mirrors the wire shape of GoalScored.
type goalScoredData struct {
	MatchGUID       string         `json:"MatchGuid"`
	MatchGUIDLow    string         `json:"matchguid"`
	Scorer          *ShortcutRef   `json:"Scorer"`
	ScorerLow       *ShortcutRef   `json:"scorer"`
	Assister        *ShortcutRef   `json:"Assister"`
	AssisterLow     *ShortcutRef   `json:"assister"`
	BallLastTouch   *ballLastTouch `json:"BallLastTouch"`
	BallLastTouchLow *ballLastTouch `json:"balllasttouch"`
	GoalSpeed       *float64       `json:"GoalSpeed"`
	GoalSpeedLow    *float64       `json:"goalspeed"`
	GoalTime        *float64       `json:"GoalTime"`
	GoalTimeLow     *float64       `json:"goaltime"`
	ImpactLocation  *vec3          `json:"ImpactLocation"`
	ImpactLocationLow *vec3        `json:"impactlocation"`
}

type enrichedGoalScored struct {
	Event           string                 `json:"Event"`
	MatchGUID       string                 `json:"matchGuid,omitempty"`
	Scorer          *EnrichedPlayer        `json:"scorer,omitempty"`
	Assister        *EnrichedPlayer        `json:"assister,omitempty"`
	BallLastTouch   *enrichedBallLastTouch `json:"ballLastTouch,omitempty"`
	GoalSpeed       *float64               `json:"goalSpeed,omitempty"`
	GoalTime        *float64               `json:"goalTime,omitempty"`
	ImpactLocation  *vec3                  `json:"impactLocation,omitempty"`
	ScoringTeam     *int                   `json:"scoringTeam,omitempty"`
	ConcedingTeam   *int                   `json:"concedingTeam,omitempty"`
	IsOwnGoal       bool                   `json:"isOwnGoal"`
	Modifiers       *goalModifiers         `json:"modifiers,omitempty"`
}

// goalModifiers carries the same-frame statfeed flags RL fires alongside
// a goal. Only flags that fire are populated; missing fields are absent
// rather than `false` so consumers can use truthy checks. Modifier
// detection is best-effort: RL's modifier statfeeds aren't fully
// documented and game-mode-specific flags (poolShot, hoopsSwishGoal)
// only exist in their respective modes.
type goalModifiers struct {
	IsAerialGoal       bool `json:"isAerialGoal,omitempty"`
	IsBackwardsGoal    bool `json:"isBackwardsGoal,omitempty"`
	IsBicycleGoal      bool `json:"isBicycleGoal,omitempty"`
	IsLongGoal         bool `json:"isLongGoal,omitempty"`
	IsTurtleGoal       bool `json:"isTurtleGoal,omitempty"`
	IsOvertimeGoal     bool `json:"isOvertimeGoal,omitempty"`
	IsPoolShot         bool `json:"isPoolShot,omitempty"`
	IsHoopsSwishGoal   bool `json:"isHoopsSwishGoal,omitempty"`
	IsHatTrickGoal     bool `json:"isHatTrickGoal,omitempty"`
	// IsFlipResetGoal is toolkit-detected, not from a Statfeed: scorer
	// got a FlipReset and stayed airborne (bOnGround=false) until
	// scoring. See flipResetArmed bookkeeping in onUpdateState +
	// emitFlipReset, consumed in onGoalScored.
	IsFlipResetGoal    bool `json:"isFlipResetGoal,omitempty"`
}

// modifierStatfeedNames maps the statfeed EventName (as RL ships it) to
// the goalModifiers boolean field that should be set. Only same-player
// matches against the scorer count.
var modifierStatfeedNames = map[string]func(*goalModifiers){
	"AerialGoal":     func(m *goalModifiers) { m.IsAerialGoal = true },
	"BackwardsGoal":  func(m *goalModifiers) { m.IsBackwardsGoal = true },
	"BicycleGoal":    func(m *goalModifiers) { m.IsBicycleGoal = true },
	"LongGoal":       func(m *goalModifiers) { m.IsLongGoal = true },
	"TurtleGoal":     func(m *goalModifiers) { m.IsTurtleGoal = true },
	"OvertimeGoal":   func(m *goalModifiers) { m.IsOvertimeGoal = true },
	"PoolShot":       func(m *goalModifiers) { m.IsPoolShot = true },
	"HoopsSwishGoal": func(m *goalModifiers) { m.IsHoopsSwishGoal = true },
	"HatTrick":       func(m *goalModifiers) { m.IsHatTrickGoal = true },
}

func (s *Synthesizer) onGoalScored(raw []byte) {
	inner := unwrapInnerData(raw)
	if inner == "" {
		return
	}
	var d goalScoredData
	if err := json.Unmarshal([]byte(inner), &d); err != nil {
		return
	}

	scorerRef := d.Scorer
	if scorerRef == nil {
		scorerRef = d.ScorerLow
	}
	// SDK has a guard: drop GoalScored events where the scorer can't be
	// identified. RL re-fires GoalScored at round-restart with empty
	// Scorer.Name; without this drop, a synthetic event with name
	// "Unknown" overwrites the prior tick's scorer for any plugin that
	// caches the latest goal. Mirror that guard here.
	if scorerRef == nil || scorerRef.Name == "" {
		return
	}
	scorer := s.roster.ResolveByShortcut(*scorerRef)
	if scorer == nil {
		return
	}

	guid := pickStr(d.MatchGUID, d.MatchGUIDLow)
	assisterRef := d.Assister
	if assisterRef == nil {
		assisterRef = d.AssisterLow
	}
	lastTouch := d.BallLastTouch
	if lastTouch == nil {
		lastTouch = d.BallLastTouchLow
	}
	loc := d.ImpactLocation
	if loc == nil {
		loc = d.ImpactLocationLow
	}

	out := enrichedGoalScored{
		Event:          "_GoalScored",
		MatchGUID:      guid,
		Scorer:         scorer,
		GoalSpeed:      pickFloat(d.GoalSpeed, d.GoalSpeedLow),
		GoalTime:       pickFloat(d.GoalTime, d.GoalTimeLow),
		ImpactLocation: loc,
	}

	// Scoring team is the scorer's team; conceding is the other.
	scoringTeam := scorer.Team
	concedingTeam := 1 - scoringTeam
	out.ScoringTeam = &scoringTeam
	out.ConcedingTeam = &concedingTeam

	if assisterRef != nil && assisterRef.Name != "" {
		out.Assister = s.roster.ResolveByShortcut(*assisterRef)
	}

	var lastToucher *EnrichedPlayer
	if lastTouch != nil {
		ref := lastTouch.Player
		if ref == nil {
			ref = lastTouch.PlayerLow
		}
		sp := pickFloat(lastTouch.Speed, lastTouch.SpeedLow)
		if ref != nil {
			lastToucher = s.roster.ResolveByShortcut(*ref)
		}
		if lastToucher != nil || sp != nil {
			out.BallLastTouch = &enrichedBallLastTouch{
				Player: lastToucher,
				Speed:  sp,
			}
		}
	}
	// Fallback: when RL ships GoalScored without a BallLastTouch block
	// (observed on some builds, especially for own goals), use the
	// most recent BallHit from the shared correlation buffer. Same
	// heuristic, just sourced from the producer that already records it.
	if lastToucher == nil {
		for _, p := range s.correlation.Recent("BallHit", 1) {
			if rec, ok := p.(*ballHitRecord); ok && rec != nil {
				lastToucher = rec.Player
			}
		}
	}
	// Own-goal heuristic: if the last-touch player is on the conceding
	// team, the goal was deflected/own-goaled. The richer _OwnGoal
	// event ships in Phase 2 with score-delta verification; this flag
	// is the cheap header.
	if lastToucher != nil && lastToucher.Team == concedingTeam {
		out.IsOwnGoal = true
	}

	// Bump the per-player real-goal counter for non-own-goals so
	// emitHatTrick can verify RL's HatTrick threshold against actual
	// scoring (RL's own counter includes own goals, which most
	// communities don't count toward a hat trick).
	if !out.IsOwnGoal && scorer.ID != "" {
		s.realGoalsMu.Lock()
		s.realGoalsByID[scorer.ID]++
		s.realGoalsMu.Unlock()
	}

	// Modifier flags via the correlation buffer. Statfeeds fire on the
	// same frame as the goal (or one before/after), so a 3-event window
	// is plenty. Match by Shortcut (RL's spectator-name identifier);
	// fall back to Name for safety.
	mods := s.collectGoalModifiers(scorerRef)

	// RL's HatTrick Statfeed counts own goals toward the threshold. If
	// our real-goal count is below 3 the player hasn't actually scored
	// a hat trick (e.g. 2 honest goals + 1 own goal = 3 by RL, 2 by us).
	// Clear the modifier flag so consumers see consistent behavior.
	if mods != nil && mods.IsHatTrickGoal && scorer.ID != "" {
		s.realGoalsMu.Lock()
		real := s.realGoalsByID[scorer.ID]
		s.realGoalsMu.Unlock()
		if real < 3 {
			mods.IsHatTrickGoal = false
		}
	}

	// Flip-reset goal: scorer was armed by a prior FlipReset and
	// hadn't touched ground since. Consume the flag so the next goal
	// from this player isn't tagged unless they earn another reset run.
	if scorer.ID != "" {
		s.flipResetArmedMu.Lock()
		armed := s.flipResetArmed[scorer.ID]
		if armed {
			delete(s.flipResetArmed, scorer.ID)
		}
		s.flipResetArmedMu.Unlock()
		if armed {
			if mods == nil {
				mods = &goalModifiers{}
			}
			mods.IsFlipResetGoal = true
		}
	}
	if mods != nil {
		out.Modifiers = mods
	}

	// Record the resolved goal into the correlation buffer so _OwnGoal
	// (Phase 2) can attach it as `correlatedGoal`. The Phase-3 _Assist
	// emitter will also read this entry.
	rec := &goalRecord{
		Scorer:        scorer,
		ScoringTeam:   scoringTeam,
		ConcedingTeam: concedingTeam,
	}
	s.correlation.Record("_GoalScored", rec)

	// Dedicated cache for _GoalReplayContext. The correlation buffer is
	// shared with BallHit/StatfeedEvent and a goal celebration can evict
	// the _GoalScored entry before GoalReplayStart arrives. Cache the
	// full enriched envelope so _GoalReplayContext can mirror every
	// field (assister, ballLastTouch, modifiers, …).
	cached := out
	s.lastGoalMu.Lock()
	s.lastGoal = &cached
	s.lastGoalMu.Unlock()

	b, err := json.Marshal(out)
	if err != nil {
		return
	}
	s.bus.Broadcast(Event{Raw: b})

	// _KickoffConverted — first scoring action within the kickoff
	// window after RoundStarted.
	s.maybeEmitKickoffConverted(guid, "GoalScored", scorer, scoringTeam)
}

// goalRecord is the slim correlation-buffer entry for _GoalScored —
// just the bits _OwnGoal / _Assist need to relate themselves to a goal.
type goalRecord struct {
	Scorer        *EnrichedPlayer
	ScoringTeam   int
	ConcedingTeam int
}

// collectGoalModifiers walks the correlation buffer for same-player
// modifier statfeeds. Returns nil when nothing matches so the encoded
// JSON omits the modifiers field entirely.
func (s *Synthesizer) collectGoalModifiers(scorer *ShortcutRef) *goalModifiers {
	if scorer == nil {
		return nil
	}
	var mods *goalModifiers

	apply := func(name string) {
		setter, ok := modifierStatfeedNames[name]
		if !ok {
			return
		}
		if mods == nil {
			mods = &goalModifiers{}
		}
		setter(mods)
	}

	// FindWithin runs the predicate against every StatfeedEvent in the
	// last 15-event window and returns the first match. We need to
	// inspect ALL same-player statfeeds, not just one — so use Recent.
	for _, p := range s.correlation.Recent("StatfeedEvent", 15) {
		rec, ok := p.(*statfeedRecord)
		if !ok {
			continue
		}
		if rec.MainRef == nil {
			continue
		}
		if !sameShortcutPlayer(rec.MainRef, scorer) {
			continue
		}
		apply(rec.EventName)
	}

	return mods
}

// sameShortcutPlayer compares two shortcut refs for "same player". RL's
// Shortcut is the per-match slot index (number); Name is the
// human-readable form. Either match counts; both unset means no match.
func sameShortcutPlayer(a, b *ShortcutRef) bool {
	if a == nil || b == nil {
		return false
	}
	if a.Shortcut != 0 && b.Shortcut != 0 && a.Shortcut == b.Shortcut {
		return true
	}
	if a.Name != "" && b.Name != "" && a.Name == b.Name {
		return true
	}
	return false
}
