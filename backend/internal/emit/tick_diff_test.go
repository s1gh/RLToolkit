package emit

import (
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
