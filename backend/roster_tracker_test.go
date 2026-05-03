package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// envelope mimics the RL Stats API wire shape: a top-level envelope
// with Event/Data, Data being a JSON-encoded string. That double encode
// matches how the toolkit forwards UpdateState through the bus.
func envelope(eventName string, inner any) []byte {
	innerBytes, _ := json.Marshal(inner)
	outer, _ := json.Marshal(struct {
		Event string `json:"Event"`
		Data  string `json:"Data"`
	}{
		Event: eventName,
		Data:  string(innerBytes),
	})
	return outer
}

// drain pulls everything currently on the channel without blocking.
// Returns the parsed _RosterChanged events in order.
func drainRoster(t *testing.T, ch <-chan []byte) []rosterEvent {
	t.Helper()
	var out []rosterEvent
	deadline := time.NewTimer(20 * time.Millisecond)
	defer deadline.Stop()
	for {
		select {
		case raw, ok := <-ch:
			if !ok {
				return out
			}
			var e rosterEvent
			if err := json.Unmarshal(raw, &e); err == nil && e.Event == "_RosterChanged" {
				out = append(out, e)
			}
		case <-deadline.C:
			return out
		}
	}
}

func TestRosterTracker_EmitsOnFirstSeen(t *testing.T) {
	bus := NewEventBus()
	tracker := NewRosterTracker(bus)
	ch, cancel := bus.Subscribe(nil)
	defer cancel()

	tracker.Feed(envelope("UpdateState", map[string]any{
		"MatchGuid": "match-1",
		"Players": []map[string]any{
			{"PrimaryId": "Steam|111|0", "Name": "Alice", "TeamNum": 0},
			{"PrimaryId": "Steam|222|0", "Name": "Bob", "TeamNum": 1},
		},
	}))

	got := drainRoster(t, ch)
	if len(got) != 1 {
		t.Fatalf("expected 1 _RosterChanged, got %d", len(got))
	}
	e := got[0]
	if e.MatchGUID != "match-1" {
		t.Errorf("MatchGUID = %q, want %q", e.MatchGUID, "match-1")
	}
	if len(e.Players) != 2 {
		t.Fatalf("len(Players) = %d, want 2", len(e.Players))
	}
	if e.Players[0].Platform != "Steam" {
		t.Errorf("platform = %q, want Steam", e.Players[0].Platform)
	}
}

func TestRosterTracker_NoEmitOnIdenticalRoster(t *testing.T) {
	bus := NewEventBus()
	tracker := NewRosterTracker(bus)
	ch, cancel := bus.Subscribe(nil)
	defer cancel()

	pkt := envelope("UpdateState", map[string]any{
		"MatchGuid": "match-1",
		"Players": []map[string]any{
			{"PrimaryId": "Steam|111|0", "Name": "Alice", "TeamNum": 0},
		},
	})
	tracker.Feed(pkt)
	first := drainRoster(t, ch)
	if len(first) != 1 {
		t.Fatalf("first feed: expected 1 event, got %d", len(first))
	}
	tracker.Feed(pkt) // identical
	second := drainRoster(t, ch)
	if len(second) != 0 {
		t.Errorf("second identical feed: expected 0 events, got %d", len(second))
	}
}

func TestRosterTracker_EmitsOnLateJoiner(t *testing.T) {
	bus := NewEventBus()
	tracker := NewRosterTracker(bus)
	ch, cancel := bus.Subscribe(nil)
	defer cancel()

	tracker.Feed(envelope("UpdateState", map[string]any{
		"MatchGuid": "m",
		"Players": []map[string]any{
			{"PrimaryId": "Steam|111|0", "Name": "A", "TeamNum": 0},
		},
	}))
	drainRoster(t, ch)
	tracker.Feed(envelope("UpdateState", map[string]any{
		"MatchGuid": "m",
		"Players": []map[string]any{
			{"PrimaryId": "Steam|111|0", "Name": "A", "TeamNum": 0},
			{"PrimaryId": "Steam|222|0", "Name": "B", "TeamNum": 1},
		},
	}))
	got := drainRoster(t, ch)
	if len(got) != 1 {
		t.Fatalf("expected 1 event on late joiner, got %d", len(got))
	}
	if len(got[0].Players) != 2 {
		t.Errorf("expected 2 players in payload, got %d", len(got[0].Players))
	}
}

func TestRosterTracker_FingerprintIgnoresOrder(t *testing.T) {
	bus := NewEventBus()
	tracker := NewRosterTracker(bus)
	ch, cancel := bus.Subscribe(nil)
	defer cancel()

	tracker.Feed(envelope("UpdateState", map[string]any{
		"MatchGuid": "m",
		"Players": []map[string]any{
			{"PrimaryId": "Steam|111|0", "Name": "A", "TeamNum": 0},
			{"PrimaryId": "Steam|222|0", "Name": "B", "TeamNum": 1},
		},
	}))
	drainRoster(t, ch)
	// Same roster, different order — fingerprint should match.
	tracker.Feed(envelope("UpdateState", map[string]any{
		"MatchGuid": "m",
		"Players": []map[string]any{
			{"PrimaryId": "Steam|222|0", "Name": "B", "TeamNum": 1},
			{"PrimaryId": "Steam|111|0", "Name": "A", "TeamNum": 0},
		},
	}))
	got := drainRoster(t, ch)
	if len(got) != 0 {
		t.Errorf("expected 0 events on reorder, got %d", len(got))
	}
}

func TestRosterTracker_NameChangeDoesNotEmit(t *testing.T) {
	bus := NewEventBus()
	tracker := NewRosterTracker(bus)
	ch, cancel := bus.Subscribe(nil)
	defer cancel()

	tracker.Feed(envelope("UpdateState", map[string]any{
		"MatchGuid": "m",
		"Players": []map[string]any{
			{"PrimaryId": "Steam|111|0", "Name": "A", "TeamNum": 0},
		},
	}))
	drainRoster(t, ch)
	// Same roster identity, name typo correction. Should NOT emit —
	// the SDK's encounter ledger handles alias drift on its side.
	tracker.Feed(envelope("UpdateState", map[string]any{
		"MatchGuid": "m",
		"Players": []map[string]any{
			{"PrimaryId": "Steam|111|0", "Name": "Alice", "TeamNum": 0},
		},
	}))
	got := drainRoster(t, ch)
	if len(got) != 0 {
		t.Errorf("expected 0 events on name change, got %d", len(got))
	}
}

func TestRosterTracker_EmitsOnGuidChange(t *testing.T) {
	bus := NewEventBus()
	tracker := NewRosterTracker(bus)
	ch, cancel := bus.Subscribe(nil)
	defer cancel()

	roster := []map[string]any{
		{"PrimaryId": "Steam|111|0", "Name": "A", "TeamNum": 0},
	}
	tracker.Feed(envelope("UpdateState", map[string]any{"MatchGuid": "m1", "Players": roster}))
	drainRoster(t, ch)
	tracker.Feed(envelope("UpdateState", map[string]any{"MatchGuid": "m2", "Players": roster}))
	got := drainRoster(t, ch)
	if len(got) != 1 {
		t.Fatalf("expected 1 event on guid change, got %d", len(got))
	}
	if got[0].MatchGUID != "m2" {
		t.Errorf("MatchGUID = %q, want m2", got[0].MatchGUID)
	}
}

func TestRosterTracker_MatchDestroyedClearsCache(t *testing.T) {
	bus := NewEventBus()
	tracker := NewRosterTracker(bus)
	ch, cancel := bus.Subscribe(nil)
	defer cancel()

	pkt := envelope("UpdateState", map[string]any{
		"MatchGuid": "m",
		"Players": []map[string]any{
			{"PrimaryId": "Steam|111|0", "Name": "A", "TeamNum": 0},
		},
	})
	tracker.Feed(pkt)
	drainRoster(t, ch)
	// MatchDestroyed clears the cache; the same UpdateState afterwards
	// should re-emit because the tracker considers it fresh.
	tracker.Feed([]byte(`{"Event":"MatchDestroyed"}`))
	tracker.Feed(pkt)
	got := drainRoster(t, ch)
	if len(got) != 1 {
		t.Errorf("expected 1 event after MatchDestroyed, got %d", len(got))
	}
}

func TestRosterTracker_IgnoresNonUpdateState(t *testing.T) {
	bus := NewEventBus()
	tracker := NewRosterTracker(bus)
	ch, cancel := bus.Subscribe(nil)
	defer cancel()

	tracker.Feed([]byte(`{"Event":"GoalScored","Data":"{}"}`))
	got := drainRoster(t, ch)
	if len(got) != 0 {
		t.Errorf("expected 0 events on non-UpdateState, got %d", len(got))
	}
}

func TestRosterTracker_LowercaseWire(t *testing.T) {
	bus := NewEventBus()
	tracker := NewRosterTracker(bus)
	ch, cancel := bus.Subscribe(nil)
	defer cancel()

	// Some RL builds ship the envelope keys lowercase. Mirror that.
	innerBytes, _ := json.Marshal(map[string]any{
		"matchguid": "m-low",
		"players": []map[string]any{
			{"primaryid": "Steam|999|0", "name": "L", "teamnum": 0},
		},
	})
	outer, _ := json.Marshal(struct {
		Event string `json:"event"`
		Data  string `json:"data"`
	}{
		Event: "updatestate",
		Data:  string(innerBytes),
	})
	tracker.Feed(outer)
	got := drainRoster(t, ch)
	if len(got) != 1 {
		t.Fatalf("expected 1 event on lowercase wire, got %d", len(got))
	}
	if got[0].MatchGUID != "m-low" {
		t.Errorf("MatchGUID = %q, want m-low", got[0].MatchGUID)
	}
	if len(got[0].Players) != 1 || got[0].Players[0].Name != "L" {
		t.Errorf("payload didn't normalize correctly: %+v", got[0])
	}
}

func TestRosterTracker_DoesntStallOnBus(t *testing.T) {
	// Sanity check — feeding many packets should never block. The
	// underlying bus drops slow subscribers rather than backpressure.
	bus := NewEventBus()
	tracker := NewRosterTracker(bus)
	// No subscriber attached — Publish should just no-op for missing
	// receivers.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			tracker.Feed(envelope("UpdateState", map[string]any{
				"MatchGuid": "m",
				"Players": []map[string]any{
					{"PrimaryId": "Steam|" + string(rune(i%26+'a')) + "|0", "Name": "A", "TeamNum": 0},
				},
			}))
		}
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("Feed loop stalled — unexpected backpressure")
	}
}
