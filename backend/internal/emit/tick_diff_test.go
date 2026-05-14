package emit

import (
	"bytes"
	"encoding/json"
	"rl-toolkit/backend/internal/bus"
	"rl-toolkit/backend/internal/types"
	"testing"
)

// boostConsumed mirrors boostPickup but on the falling edge: every
// time a player's Boost decreased between ticks (active boost spend),
// emit _BoostConsumed with the spent amount. Plugins that want to
// track total boost burned over a session can sum the deltas without
// keeping their own per-tick state.

func intp(v int) *int { return &v }

func TestTickDiff_BoostConsumed_FiresOnDecrease(t *testing.T) {
	td := &TickDiff{}
	prev := &types.TickPlayer{ID: "p1", Name: "me", Team: 0, Boost: intp(80)}
	curr := &types.TickPlayer{ID: "p1", Name: "me", Team: 0, Boost: intp(45)}

	got := td.boostConsumed("g1", prev, curr)
	if got == nil {
		t.Fatal("expected _BoostConsumed event, got nil")
	}
	if got.Name != "_BoostConsumed" {
		t.Errorf("event name = %q, want _BoostConsumed", got.Name)
	}
	var payload struct {
		MatchGUID   string `json:"matchGuid"`
		BoostBefore int    `json:"boostBefore"`
		BoostAfter  int    `json:"boostAfter"`
		Delta       int    `json:"delta"`
		Player      struct {
			ID string `json:"id"`
		} `json:"player"`
	}
	if err := json.Unmarshal(got.Data, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.BoostBefore != 80 || payload.BoostAfter != 45 || payload.Delta != 35 {
		t.Errorf("boost fields wrong: %+v", payload)
	}
	if payload.Player.ID != "p1" {
		t.Errorf("player id = %q, want p1", payload.Player.ID)
	}
}

func TestTickDiff_BoostConsumed_SkipsWhenIncreased(t *testing.T) {
	td := &TickDiff{}
	prev := &types.TickPlayer{ID: "p1", Boost: intp(20)}
	curr := &types.TickPlayer{ID: "p1", Boost: intp(75)}
	if got := td.boostConsumed("g1", prev, curr); got != nil {
		t.Errorf("expected nil on boost increase, got event")
	}
}

func TestTickDiff_BoostConsumed_SkipsWhenUnchanged(t *testing.T) {
	td := &TickDiff{}
	prev := &types.TickPlayer{ID: "p1", Boost: intp(33)}
	curr := &types.TickPlayer{ID: "p1", Boost: intp(33)}
	if got := td.boostConsumed("g1", prev, curr); got != nil {
		t.Errorf("expected nil on no change, got event")
	}
}

// Respawn drops boost from current value to 33 (default spawn boost).
// The respawn drop isn't an "active spend" — it's a system reset, so
// boostConsumed should suppress it. Mirrors the demolished-edge
// suppression in boostPickup.
func TestTickDiff_BoostConsumed_SuppressesRespawnDrop(t *testing.T) {
	td := &TickDiff{}
	prev := &types.TickPlayer{ID: "p1", Boost: intp(80), Demolished: false}
	curr := &types.TickPlayer{ID: "p1", Boost: intp(33), Demolished: false, OnGround: true}
	// Was demolished on a previous tick; respawn flips Demolished to false
	// and reseeds boost. We simulate the post-respawn tick:
	prev.Demolished = true
	if got := td.boostConsumed("g1", prev, curr); got != nil {
		t.Errorf("expected nil on respawn boost reset, got event")
	}
}

func TestTickDiff_BoostConsumed_NilCurrentBoostNoOp(t *testing.T) {
	td := &TickDiff{}
	prev := &types.TickPlayer{ID: "p1", Boost: intp(50)}
	curr := &types.TickPlayer{ID: "p1", Boost: nil}
	if got := td.boostConsumed("g1", prev, curr); got != nil {
		t.Errorf("expected nil when curr.Boost is nil, got event")
	}
}

// stubResolver mirrors what the live roster.Tracker does: stamps
// IsMe when the resolved ID matches the configured "me" id.
type stubResolver struct{ meID string }

func (s stubResolver) ResolveByPrimaryId(id string) *types.EnrichedPlayer {
	return &types.EnrichedPlayer{ID: id, IsMe: id == s.meID}
}

// IsMe must be stamped on the emitted player payload — without it,
// any plugin filtering by isMe silently drops every event. Earlier
// versions of boostConsumed/boostPickup built EnrichedPlayer
// manually and skipped the roster's stampIsMe step.
func TestTickDiff_BoostConsumed_StampsIsMeViaResolver(t *testing.T) {
	td := &TickDiff{roster: stubResolver{meID: "me-id"}}
	prev := &types.TickPlayer{ID: "me-id", Boost: intp(80)}
	curr := &types.TickPlayer{ID: "me-id", Boost: intp(45)}
	got := td.boostConsumed("g1", prev, curr)
	if got == nil {
		t.Fatal("expected event")
	}
	var payload struct {
		Player struct {
			ID   string `json:"id"`
			IsMe bool   `json:"isMe"`
		} `json:"player"`
	}
	if err := json.Unmarshal(got.Data, &payload); err != nil {
		t.Fatal(err)
	}
	if !payload.Player.IsMe {
		t.Errorf("IsMe = false on resolved local player; payload: %s", got.Data)
	}
}

func TestTickDiff_BoostConsumed_LeavesIsMeFalseForOthers(t *testing.T) {
	td := &TickDiff{roster: stubResolver{meID: "me-id"}}
	prev := &types.TickPlayer{ID: "other-id", Boost: intp(80)}
	curr := &types.TickPlayer{ID: "other-id", Boost: intp(45)}
	got := td.boostConsumed("g1", prev, curr)
	if got == nil {
		t.Fatal("expected event")
	}
	if bytes.Contains(got.Data, []byte(`"isMe":true`)) {
		t.Errorf("opponent payload should not have isMe:true: %s", got.Data)
	}
}

// Respawn suppression: the boost reseed (whatever→33 default) lands a
// tick or two AFTER prev.Demolished flips back to false, so the
// per-tick prev/curr.Demolished check alone misses it. TickDiff has
// to carry a one-shot "we owe a respawn suppression for this player"
// flag, set when we observe Demolished true→false, consumed on the
// next boost decrease for that player.
func TestTickDiff_BoostConsumed_SuppressesPostRespawnDropOnLaterTick(t *testing.T) {
	td := &TickDiff{}
	id := "p1"
	// Frame N: respawn lands. Demolished flipped true→false, boost is
	// still whatever it was before death (74). No drop yet.
	prev1 := &types.TickPlayer{ID: id, Boost: intp(74), Demolished: true}
	curr1 := &types.TickPlayer{ID: id, Boost: intp(74), Demolished: false}
	if got := td.boostConsumed("g1", prev1, curr1); got != nil {
		t.Errorf("frame N (no drop yet) shouldn't emit, got %s", got.Data)
	}
	// Frame N+1: RL reseeds boost to 33. Both ticks alive; under the
	// old logic this would emit a 41-boost spend.
	prev2 := &types.TickPlayer{ID: id, Boost: intp(74), Demolished: false}
	curr2 := &types.TickPlayer{ID: id, Boost: intp(33), Demolished: false}
	if got := td.boostConsumed("g1", prev2, curr2); got != nil {
		t.Errorf("post-respawn boost reseed should be suppressed, got %s", got.Data)
	}
	// Frame N+2: real boost spend. Suppression is one-shot, so this
	// fires normally.
	prev3 := &types.TickPlayer{ID: id, Boost: intp(33), Demolished: false}
	curr3 := &types.TickPlayer{ID: id, Boost: intp(28), Demolished: false}
	if got := td.boostConsumed("g1", prev3, curr3); got == nil {
		t.Errorf("real spend after respawn-suppression should emit")
	}
}

// Match boundary clears any pending suppression so a stale flag from
// the previous match can't swallow the first real spend of the next.
func TestTickDiff_BoostConsumed_SuppressionResetsBetweenMatches(t *testing.T) {
	td := &TickDiff{}
	id := "p1"
	// Arm suppression in match A.
	td.boostConsumed("matchA", &types.TickPlayer{ID: id, Demolished: true, Boost: intp(50)},
		&types.TickPlayer{ID: id, Demolished: false, Boost: intp(50)})
	// Different match guid; suppression should not carry over.
	td.matchEnded("matchA")
	got := td.boostConsumed("matchB", &types.TickPlayer{ID: id, Boost: intp(50), Demolished: false},
		&types.TickPlayer{ID: id, Boost: intp(33), Demolished: false})
	if got == nil {
		t.Errorf("boost drop in fresh match should NOT be suppressed by stale flag")
	}
}

// boostPickup: a single physical pad pickup ramps the Boost field over
// 2-3 ticks. We coalesce that into a single _BoostPickup event emitted
// on the first non-rising tick after the streak.

func TestTickDiff_BoostPickup_NoEventWhileRising(t *testing.T) {
	td := &TickDiff{pickupStreaks: map[string]*pickupStreak{}}
	id := "p1"
	// Three consecutive rising ticks: 0 → 4 → 8 → 12. No event yet.
	if got := td.boostPickup("g", &types.TickPlayer{ID: id, Boost: intp(0)}, &types.TickPlayer{ID: id, Boost: intp(4)}); got != nil {
		t.Errorf("rising tick 1: expected nil, got event")
	}
	if got := td.boostPickup("g", &types.TickPlayer{ID: id, Boost: intp(4)}, &types.TickPlayer{ID: id, Boost: intp(8)}); got != nil {
		t.Errorf("rising tick 2: expected nil, got event")
	}
	if got := td.boostPickup("g", &types.TickPlayer{ID: id, Boost: intp(8)}, &types.TickPlayer{ID: id, Boost: intp(12)}); got != nil {
		t.Errorf("rising tick 3: expected nil, got event")
	}
	if s := td.pickupStreaks[id]; s == nil || s.startBoost != 0 || s.lastBoost != 12 {
		t.Errorf("streak state wrong: %+v", s)
	}
}

func TestTickDiff_BoostPickup_FlushesOnPlateau(t *testing.T) {
	td := &TickDiff{pickupStreaks: map[string]*pickupStreak{}}
	id := "p1"
	// Ramp 0 → 6 → 12, then plateau at 12. Flush on the plateau tick.
	td.boostPickup("g", &types.TickPlayer{ID: id, Boost: intp(0)}, &types.TickPlayer{ID: id, Boost: intp(6)})
	td.boostPickup("g", &types.TickPlayer{ID: id, Boost: intp(6)}, &types.TickPlayer{ID: id, Boost: intp(12)})
	got := td.boostPickup("g", &types.TickPlayer{ID: id, Boost: intp(12)}, &types.TickPlayer{ID: id, Boost: intp(12)})
	if got == nil {
		t.Fatal("plateau after rise should flush a _BoostPickup event")
	}
	if got.Name != "_BoostPickup" {
		t.Errorf("event name = %q, want _BoostPickup", got.Name)
	}
	var payload struct {
		BoostBefore int `json:"boostBefore"`
		BoostAfter  int `json:"boostAfter"`
		Delta       int `json:"delta"`
	}
	if err := json.Unmarshal(got.Data, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.BoostBefore != 0 || payload.BoostAfter != 12 || payload.Delta != 12 {
		t.Errorf("boost fields wrong: %+v", payload)
	}
	if _, still := td.pickupStreaks[id]; still {
		t.Errorf("streak should be cleared after flush")
	}
}

func TestTickDiff_BoostPickup_FlushesOnConsume(t *testing.T) {
	td := &TickDiff{pickupStreaks: map[string]*pickupStreak{}}
	id := "p1"
	// Ramp 0 → 33 over two ticks, then immediately start consuming.
	// The first falling tick should flush the pickup.
	td.boostPickup("g", &types.TickPlayer{ID: id, Boost: intp(0)}, &types.TickPlayer{ID: id, Boost: intp(20)})
	td.boostPickup("g", &types.TickPlayer{ID: id, Boost: intp(20)}, &types.TickPlayer{ID: id, Boost: intp(33)})
	got := td.boostPickup("g", &types.TickPlayer{ID: id, Boost: intp(33)}, &types.TickPlayer{ID: id, Boost: intp(28)})
	if got == nil {
		t.Fatal("falling tick after rise should flush a _BoostPickup event")
	}
	var payload struct {
		BoostBefore int `json:"boostBefore"`
		BoostAfter  int `json:"boostAfter"`
		Delta       int `json:"delta"`
	}
	if err := json.Unmarshal(got.Data, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.BoostBefore != 0 || payload.BoostAfter != 33 || payload.Delta != 33 {
		t.Errorf("boost fields wrong: %+v", payload)
	}
}

// isBigPad must be true when the cumulative rise is >=20 (big pads
// always give more than the 12 a small pad caps at). Plugins rely on
// this flag instead of guessing from boostAfter, because RL clamps
// mid-rise: picking up a big pad while consuming boost can land
// lastBoost at 88, 92, 95, etc.
func TestTickDiff_BoostPickup_IsBigPadTrueOnLargeRise(t *testing.T) {
	td := &TickDiff{pickupStreaks: map[string]*pickupStreak{}}
	id := "p1"
	// Rise of 88 (clearly big pad, e.g. picked up at 0 boost while moving).
	td.boostPickup("g", &types.TickPlayer{ID: id, Boost: intp(0)}, &types.TickPlayer{ID: id, Boost: intp(40)})
	td.boostPickup("g", &types.TickPlayer{ID: id, Boost: intp(40)}, &types.TickPlayer{ID: id, Boost: intp(88)})
	got := td.boostPickup("g", &types.TickPlayer{ID: id, Boost: intp(88)}, &types.TickPlayer{ID: id, Boost: intp(88)})
	if got == nil {
		t.Fatal("expected flush event")
	}
	var payload struct {
		BoostBefore int  `json:"boostBefore"`
		BoostAfter  int  `json:"boostAfter"`
		Delta       int  `json:"delta"`
		IsBigPad    bool `json:"isBigPad"`
	}
	if err := json.Unmarshal(got.Data, &payload); err != nil {
		t.Fatal(err)
	}
	if !payload.IsBigPad {
		t.Errorf("rise of 88 should be isBigPad=true, got payload %+v", payload)
	}
}

// Small pads top out at 12. A rise of <20 must be classified as a
// small pad pickup (isBigPad=false).
func TestTickDiff_BoostPickup_IsBigPadFalseOnSmallRise(t *testing.T) {
	td := &TickDiff{pickupStreaks: map[string]*pickupStreak{}}
	id := "p1"
	// 0 → 12 over two ticks: textbook small pad.
	td.boostPickup("g", &types.TickPlayer{ID: id, Boost: intp(0)}, &types.TickPlayer{ID: id, Boost: intp(6)})
	td.boostPickup("g", &types.TickPlayer{ID: id, Boost: intp(6)}, &types.TickPlayer{ID: id, Boost: intp(12)})
	got := td.boostPickup("g", &types.TickPlayer{ID: id, Boost: intp(12)}, &types.TickPlayer{ID: id, Boost: intp(12)})
	if got == nil {
		t.Fatal("expected flush event")
	}
	var payload struct {
		Delta    int  `json:"delta"`
		IsBigPad bool `json:"isBigPad"`
	}
	if err := json.Unmarshal(got.Data, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.IsBigPad {
		t.Errorf("rise of 12 should be isBigPad=false, got payload %+v", payload)
	}
}

// Threshold is >=20, so a rise of exactly 20 must be classified as a
// big pad. This is the boundary case.
func TestTickDiff_BoostPickup_IsBigPadTrueAtBoundary(t *testing.T) {
	td := &TickDiff{pickupStreaks: map[string]*pickupStreak{}}
	id := "p1"
	// 13 → 33 = delta 20 exactly. Boundary case.
	td.boostPickup("g", &types.TickPlayer{ID: id, Boost: intp(13)}, &types.TickPlayer{ID: id, Boost: intp(25)})
	td.boostPickup("g", &types.TickPlayer{ID: id, Boost: intp(25)}, &types.TickPlayer{ID: id, Boost: intp(33)})
	got := td.boostPickup("g", &types.TickPlayer{ID: id, Boost: intp(33)}, &types.TickPlayer{ID: id, Boost: intp(33)})
	if got == nil {
		t.Fatal("expected flush event")
	}
	var payload struct {
		Delta    int  `json:"delta"`
		IsBigPad bool `json:"isBigPad"`
	}
	if err := json.Unmarshal(got.Data, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Delta != 20 {
		t.Fatalf("setup: expected delta=20, got %d", payload.Delta)
	}
	if !payload.IsBigPad {
		t.Errorf("rise of exactly 20 should be isBigPad=true (threshold is >=20)")
	}
}

func TestTickDiff_BoostPickup_NoEventWithoutStreak(t *testing.T) {
	td := &TickDiff{pickupStreaks: map[string]*pickupStreak{}}
	id := "p1"
	// Boost stable, then falling — no rising tick ever happened, so
	// nothing to flush. Mirrors a player who only spends boost in a
	// given window.
	if got := td.boostPickup("g", &types.TickPlayer{ID: id, Boost: intp(50)}, &types.TickPlayer{ID: id, Boost: intp(50)}); got != nil {
		t.Errorf("plateau with no prior rise should emit nothing")
	}
	if got := td.boostPickup("g", &types.TickPlayer{ID: id, Boost: intp(50)}, &types.TickPlayer{ID: id, Boost: intp(40)}); got != nil {
		t.Errorf("fall with no prior rise should emit nothing")
	}
}

func TestTickDiff_BoostPickup_RespawnEdgeDropsStreak(t *testing.T) {
	td := &TickDiff{pickupStreaks: map[string]*pickupStreak{}}
	id := "p1"
	// Player was demolished. The Demolished true→false tick brings
	// boost from 0 → 33 (RL reseed). That's not a pickup; we must
	// not start a streak.
	got := td.boostPickup("g",
		&types.TickPlayer{ID: id, Demolished: true, Boost: intp(0)},
		&types.TickPlayer{ID: id, Demolished: false, Boost: intp(33)})
	if got != nil {
		t.Errorf("respawn reseed should not emit, got event")
	}
	if _, has := td.pickupStreaks[id]; has {
		t.Errorf("respawn reseed should not open a streak")
	}
}

func TestTickDiff_BoostPickup_NilCurrentBoostDropsStreak(t *testing.T) {
	td := &TickDiff{pickupStreaks: map[string]*pickupStreak{}}
	id := "p1"
	td.boostPickup("g", &types.TickPlayer{ID: id, Boost: intp(0)}, &types.TickPlayer{ID: id, Boost: intp(8)})
	if _, has := td.pickupStreaks[id]; !has {
		t.Fatal("setup: expected streak to be open")
	}
	got := td.boostPickup("g", &types.TickPlayer{ID: id, Boost: intp(8)}, &types.TickPlayer{ID: id, Boost: nil})
	if got != nil {
		t.Errorf("nil curr.Boost should drop the streak without emitting, got event")
	}
	if _, has := td.pickupStreaks[id]; has {
		t.Errorf("nil curr.Boost should clear the streak")
	}
}

func TestTickDiff_BoostPickup_StreakResetsBetweenMatches(t *testing.T) {
	td := &TickDiff{
		pendingRespawnSuppress: map[string]bool{},
		pickupStreaks:          map[string]*pickupStreak{},
	}
	id := "p1"
	td.boostPickup("matchA", &types.TickPlayer{ID: id, Boost: intp(0)}, &types.TickPlayer{ID: id, Boost: intp(20)})
	td.matchEnded("matchA")
	if _, has := td.pickupStreaks[id]; has {
		t.Errorf("match boundary should clear in-flight pickup streaks")
	}
}

func TestTickDiff_BoostPickup_StampsIsMeViaResolver(t *testing.T) {
	td := &TickDiff{
		roster:        stubResolver{meID: "me-id"},
		pickupStreaks: map[string]*pickupStreak{},
	}
	td.boostPickup("g", &types.TickPlayer{ID: "me-id", Boost: intp(0)}, &types.TickPlayer{ID: "me-id", Boost: intp(33)})
	got := td.boostPickup("g", &types.TickPlayer{ID: "me-id", Boost: intp(33)}, &types.TickPlayer{ID: "me-id", Boost: intp(33)})
	if got == nil {
		t.Fatal("plateau flush expected")
	}
	var payload struct {
		Player struct {
			ID   string `json:"id"`
			IsMe bool   `json:"isMe"`
		} `json:"player"`
	}
	if err := json.Unmarshal(got.Data, &payload); err != nil {
		t.Fatal(err)
	}
	if !payload.Player.IsMe {
		t.Errorf("isMe should be stamped via resolver, got %+v", payload.Player)
	}
}

// stubTickHistory exposes a latest (and optional previous) snapshot.
// previous is only needed for tests that exercise UpdateState diffing.
type stubTickHistory struct {
	latest, previous *types.TickSnapshot
}

func (s stubTickHistory) Latest() *types.TickSnapshot   { return s.latest }
func (s stubTickHistory) Previous() *types.TickSnapshot { return s.previous }

// CountdownBegin marks the start of a kickoff. RL resets every
// player's Boost to 33 between CountdownBegin and RoundStarted, so
// the next boost decrease for each player must be swallowed —
// otherwise a 80→33 reset shows up as a 47-point spend.
func TestTickDiff_Kickoff_ArmsSuppressionForPlayersAboveSpawn(t *testing.T) {
	tick := &types.TickSnapshot{
		Players: []types.TickPlayer{
			{ID: "p1", Boost: intp(80)}, // above spawn → arm
			{ID: "p2", Boost: intp(20)}, // below spawn → no arm needed
			{ID: "p3", Boost: intp(33)}, // exactly spawn → no decrease, no arm
			{ID: "p4", Boost: nil},      // unknown → skip
			{ID: "", Boost: intp(50)},   // empty ID → skip
		},
	}
	td := &TickDiff{
		ticks:                  stubTickHistory{latest: tick},
		pendingRespawnSuppress: map[string]bool{},
	}
	td.Process(bus.Event{Name: "CountdownBegin"})

	if !td.pendingRespawnSuppress["p1"] {
		t.Errorf("p1 (80 boost) should be armed for kickoff suppression")
	}
	if td.pendingRespawnSuppress["p2"] {
		t.Errorf("p2 (20 boost) should NOT be armed — won't decrease")
	}
	if td.pendingRespawnSuppress["p3"] {
		t.Errorf("p3 (33 boost) should NOT be armed — already at spawn")
	}
	if td.pendingRespawnSuppress["p4"] {
		t.Errorf("p4 (nil Boost) should NOT be armed")
	}
}

func TestTickDiff_Kickoff_DropsPickupStreaks(t *testing.T) {
	tick := &types.TickSnapshot{
		Players: []types.TickPlayer{{ID: "p1", Boost: intp(80)}},
	}
	td := &TickDiff{
		ticks:                  stubTickHistory{latest: tick},
		pendingRespawnSuppress: map[string]bool{},
		pickupStreaks:          map[string]*pickupStreak{"p1": {startBoost: 0, lastBoost: 12}},
	}
	td.Process(bus.Event{Name: "CountdownBegin"})
	if _, has := td.pickupStreaks["p1"]; has {
		t.Errorf("kickoff should drop in-flight pickup streaks")
	}
}

// Kickoff suppression must consume on the first decrease. The
// post-reset value is 33 for everyone; the suppressed event would
// represent "80→33 = 47 spent" which is bogus.
func TestTickDiff_Kickoff_SwallowsResetDecrease(t *testing.T) {
	tick := &types.TickSnapshot{
		Players: []types.TickPlayer{{ID: "p1", Boost: intp(80)}},
	}
	td := &TickDiff{
		ticks:                  stubTickHistory{latest: tick},
		pendingRespawnSuppress: map[string]bool{},
	}
	td.Process(bus.Event{Name: "CountdownBegin"})

	got := td.boostConsumed("g",
		&types.TickPlayer{ID: "p1", Boost: intp(80)},
		&types.TickPlayer{ID: "p1", Boost: intp(33)})
	if got != nil {
		t.Errorf("kickoff reset 80→33 should be swallowed, got event")
	}
	if td.pendingRespawnSuppress["p1"] {
		t.Errorf("flag should clear after one-shot consumption")
	}
}

// updateStateRaw fabricates a minimal UpdateState envelope. The
// tick-diff Process path only needs the event name to dispatch; the
// Latest/Previous snapshots come from the injected TickHistory stub.
func updateStateRaw() []byte { return []byte(`{}`) }

// Real-match -> freeplay can land a boundary tick where one side has
// the previous match's GUID and the other has an empty GUID (freeplay
// hasn't settled yet, or the post-match tick lost its guid first).
// The original guard required BOTH sides non-empty before suppressing
// the diff, so this case slipped through and produced a negative
// _PlayerScoreChanged delta (Goals 3 → 0 = -3) that stat plugins
// applied as a decrement on the user's session totals.
func TestTickDiff_Process_GuidGoingEmptySuppressesDiff(t *testing.T) {
	prev := &types.TickSnapshot{
		MatchGUID: "match-A",
		Players:   []types.TickPlayer{{ID: "me", Goals: 3, Saves: 2}},
	}
	curr := &types.TickSnapshot{
		MatchGUID: "",
		Players:   []types.TickPlayer{{ID: "me", Goals: 0, Saves: 0}},
	}
	td := &TickDiff{
		phase:                  stubPhase{phase: types.PhaseLive},
		ticks:                  stubTickHistory{latest: curr, previous: prev},
		pendingRespawnSuppress: map[string]bool{},
		pickupStreaks:          map[string]*pickupStreak{},
	}
	out := td.Process(bus.Event{Name: "UpdateState", Raw: updateStateRaw()})
	for _, e := range out {
		if e.Name == "_PlayerScoreChanged" {
			t.Fatalf("guid going empty must suppress diff, got %s", e.Name)
		}
	}
}

// Mirror of the above for the empty -> non-empty direction (freeplay
// settling into a fresh match guid). Same risk: a stat reset would be
// emitted as a positive delta and double-count.
func TestTickDiff_Process_GuidComingFromEmptySuppressesDiff(t *testing.T) {
	prev := &types.TickSnapshot{
		MatchGUID: "",
		Players:   []types.TickPlayer{{ID: "me", Goals: 0}},
	}
	curr := &types.TickSnapshot{
		MatchGUID: "match-B",
		Players:   []types.TickPlayer{{ID: "me", Goals: 3}},
	}
	td := &TickDiff{
		phase:                  stubPhase{phase: types.PhaseLive},
		ticks:                  stubTickHistory{latest: curr, previous: prev},
		pendingRespawnSuppress: map[string]bool{},
		pickupStreaks:          map[string]*pickupStreak{},
	}
	out := td.Process(bus.Event{Name: "UpdateState", Raw: updateStateRaw()})
	for _, e := range out {
		if e.Name == "_PlayerScoreChanged" {
			t.Fatalf("guid arriving from empty must suppress diff, got %s", e.Name)
		}
	}
}

// Same-guid ticks must still diff normally — the guard widening is a
// no-op for the common case.
func TestTickDiff_Process_SameGuidStillDiffs(t *testing.T) {
	prev := &types.TickSnapshot{
		MatchGUID: "match-A",
		Players:   []types.TickPlayer{{ID: "me", Goals: 1}},
	}
	curr := &types.TickSnapshot{
		MatchGUID: "match-A",
		Players:   []types.TickPlayer{{ID: "me", Goals: 2}},
	}
	td := &TickDiff{
		phase:                  stubPhase{phase: types.PhaseLive},
		ticks:                  stubTickHistory{latest: curr, previous: prev},
		pendingRespawnSuppress: map[string]bool{},
		pickupStreaks:          map[string]*pickupStreak{},
	}
	out := td.Process(bus.Event{Name: "UpdateState", Raw: updateStateRaw()})
	saw := false
	for _, e := range out {
		if e.Name == "_PlayerScoreChanged" {
			saw = true
			break
		}
	}
	if !saw {
		t.Fatalf("same-guid tick should still emit _PlayerScoreChanged")
	}
}

// RoundStarted clears any kickoff flags still armed — without that,
// a player at <=33 boost who never saw a decrease at kickoff would
// have a stale flag that swallows their first real spend later.
func TestTickDiff_RoundStarted_ClearsArmedFlags(t *testing.T) {
	td := &TickDiff{
		pendingRespawnSuppress: map[string]bool{"p1": true, "p2": true},
	}
	td.Process(bus.Event{Name: "RoundStarted"})
	if len(td.pendingRespawnSuppress) != 0 {
		t.Errorf("RoundStarted should clear all kickoff flags, still have: %+v", td.pendingRespawnSuppress)
	}
}
