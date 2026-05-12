package emit

import (
	"bytes"
	"encoding/json"
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
