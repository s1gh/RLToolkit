package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"
	"strings"
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

// feedRaw decodes a raw RL envelope into the typed Event shape the
// pipeline produces and feeds it through RosterTracker as both a
// state and an emit processor — same pattern as the real pipeline,
// just with the bus broadcast inlined here so existing tests can
// keep subscribing to a bus channel.
func feedRaw(t *testing.T, tracker *RosterTracker, bus *Bus, raw []byte) {
	t.Helper()
	evt := Event{Name: extractEventName(raw), Raw: raw}
	tracker.Observe(evt)
	for _, out := range tracker.Process(evt) {
		bus.Broadcast(out)
	}
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
			// Decode the envelope ({Event, Data}) and re-extract the
			// roster payload from Data — the new wire shape nests it.
			var env struct {
				Event string          `json:"Event"`
				Data  json.RawMessage `json:"Data"`
			}
			if err := json.Unmarshal(raw, &env); err != nil || env.Event != "_RosterChanged" {
				continue
			}
			var payload struct {
				MatchGUID string         `json:"matchGuid"`
				Players   []rosterPlayer `json:"players"`
			}
			_ = json.Unmarshal(env.Data, &payload)
			out = append(out, rosterEvent{
				Event:     env.Event,
				MatchGUID: payload.MatchGUID,
				Players:   payload.Players,
			})
		case <-deadline.C:
			return out
		}
	}
}

func TestRosterTracker_EmitsOnFirstSeen(t *testing.T) {
	bus := NewBus()
	tracker := NewRosterTracker(bus)
	ch, cancel := bus.Subscribe(nil)
	defer cancel()

	feedRaw(t, tracker, bus, envelope("UpdateState", map[string]any{
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
	bus := NewBus()
	tracker := NewRosterTracker(bus)
	ch, cancel := bus.Subscribe(nil)
	defer cancel()

	pkt := envelope("UpdateState", map[string]any{
		"MatchGuid": "match-1",
		"Players": []map[string]any{
			{"PrimaryId": "Steam|111|0", "Name": "Alice", "TeamNum": 0},
		},
	})
	feedRaw(t, tracker, bus, pkt)
	first := drainRoster(t, ch)
	if len(first) != 1 {
		t.Fatalf("first feed: expected 1 event, got %d", len(first))
	}
	feedRaw(t, tracker, bus, pkt) // identical
	second := drainRoster(t, ch)
	if len(second) != 0 {
		t.Errorf("second identical feed: expected 0 events, got %d", len(second))
	}
}

func TestRosterTracker_EmitsOnLateJoiner(t *testing.T) {
	bus := NewBus()
	tracker := NewRosterTracker(bus)
	ch, cancel := bus.Subscribe(nil)
	defer cancel()

	feedRaw(t, tracker, bus, envelope("UpdateState", map[string]any{
		"MatchGuid": "m",
		"Players": []map[string]any{
			{"PrimaryId": "Steam|111|0", "Name": "A", "TeamNum": 0},
		},
	}))
	drainRoster(t, ch)
	feedRaw(t, tracker, bus, envelope("UpdateState", map[string]any{
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
	bus := NewBus()
	tracker := NewRosterTracker(bus)
	ch, cancel := bus.Subscribe(nil)
	defer cancel()

	feedRaw(t, tracker, bus, envelope("UpdateState", map[string]any{
		"MatchGuid": "m",
		"Players": []map[string]any{
			{"PrimaryId": "Steam|111|0", "Name": "A", "TeamNum": 0},
			{"PrimaryId": "Steam|222|0", "Name": "B", "TeamNum": 1},
		},
	}))
	drainRoster(t, ch)
	// Same roster, different order — fingerprint should match.
	feedRaw(t, tracker, bus, envelope("UpdateState", map[string]any{
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
	bus := NewBus()
	tracker := NewRosterTracker(bus)
	ch, cancel := bus.Subscribe(nil)
	defer cancel()

	feedRaw(t, tracker, bus, envelope("UpdateState", map[string]any{
		"MatchGuid": "m",
		"Players": []map[string]any{
			{"PrimaryId": "Steam|111|0", "Name": "A", "TeamNum": 0},
		},
	}))
	drainRoster(t, ch)
	// Same roster identity, name typo correction. Should NOT emit —
	// the SDK's encounter ledger handles alias drift on its side.
	feedRaw(t, tracker, bus, envelope("UpdateState", map[string]any{
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
	bus := NewBus()
	tracker := NewRosterTracker(bus)
	ch, cancel := bus.Subscribe(nil)
	defer cancel()

	roster := []map[string]any{
		{"PrimaryId": "Steam|111|0", "Name": "A", "TeamNum": 0},
	}
	feedRaw(t, tracker, bus, envelope("UpdateState", map[string]any{"MatchGuid": "m1", "Players": roster}))
	drainRoster(t, ch)
	feedRaw(t, tracker, bus, envelope("UpdateState", map[string]any{"MatchGuid": "m2", "Players": roster}))
	got := drainRoster(t, ch)
	if len(got) != 1 {
		t.Fatalf("expected 1 event on guid change, got %d", len(got))
	}
	if got[0].MatchGUID != "m2" {
		t.Errorf("MatchGUID = %q, want m2", got[0].MatchGUID)
	}
}

func TestRosterTracker_MatchDestroyedClearsCache(t *testing.T) {
	bus := NewBus()
	tracker := NewRosterTracker(bus)
	ch, cancel := bus.Subscribe(nil)
	defer cancel()

	pkt := envelope("UpdateState", map[string]any{
		"MatchGuid": "m",
		"Players": []map[string]any{
			{"PrimaryId": "Steam|111|0", "Name": "A", "TeamNum": 0},
		},
	})
	feedRaw(t, tracker, bus, pkt)
	drainRoster(t, ch)
	// MatchDestroyed clears the cache AND emits an empty roster so plugins
	// clear their match view. The same UpdateState afterwards should re-emit
	// because the tracker considers it fresh.
	feedRaw(t, tracker, bus, []byte(`{"Event":"MatchDestroyed"}`))
	feedRaw(t, tracker, bus, pkt)
	got := drainRoster(t, ch)
	if len(got) != 2 {
		t.Errorf("expected 2 events after MatchDestroyed (empty + re-emit), got %d", len(got))
	}
	if len(got) >= 1 && len(got[0].Players) != 0 {
		t.Errorf("first event after MatchDestroyed should be empty roster, got %d players", len(got[0].Players))
	}
	if len(got) >= 2 && len(got[1].Players) != 1 {
		t.Errorf("second event after MatchDestroyed should re-emit 1 player, got %d", len(got[1].Players))
	}
}

func TestRosterTracker_IgnoresNonUpdateState(t *testing.T) {
	bus := NewBus()
	tracker := NewRosterTracker(bus)
	ch, cancel := bus.Subscribe(nil)
	defer cancel()

	feedRaw(t, tracker, bus, []byte(`{"Event":"GoalScored","Data":"{}"}`))
	got := drainRoster(t, ch)
	if len(got) != 0 {
		t.Errorf("expected 0 events on non-UpdateState, got %d", len(got))
	}
}

func TestRosterTracker_LowercaseWire(t *testing.T) {
	bus := NewBus()
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
	feedRaw(t, tracker, bus, outer)
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

// TestRosterTracker_RewritesBotIds asserts that when RL ships multiple
// bots all under "Unknown|0|0", the tracker mints distinct per-bot ids
// derived from the Name. Without this, every bot collapses into one
// roster entry / one ledger row / one synthetic-event subject.
func TestRosterTracker_RewritesBotIds(t *testing.T) {
	bus := NewBus()
	tracker := NewRosterTracker(bus)
	ch, cancel := bus.Subscribe(nil)
	defer cancel()

	feedRaw(t, tracker, bus, envelope("UpdateState", map[string]any{
		"MatchGuid": "bot-match",
		"Players": []map[string]any{
			{"PrimaryId": "Steam|111|0", "Name": "s1gh", "TeamNum": 0},
			{"PrimaryId": "Unknown|0|0", "Name": "Roundhouse", "TeamNum": 1},
			{"PrimaryId": "Unknown|0|0", "Name": "Merlin", "TeamNum": 1},
		},
	}))

	got := drainRoster(t, ch)
	if len(got) != 1 {
		t.Fatalf("expected 1 _RosterChanged, got %d", len(got))
	}
	if len(got[0].Players) != 3 {
		t.Fatalf("expected 3 players in payload, got %d", len(got[0].Players))
	}

	// Build an id-set; every player should land under a distinct id.
	ids := map[string]bool{}
	var roundhouseID, merlinID string
	for _, p := range got[0].Players {
		ids[p.ID] = true
		switch p.Name {
		case "Roundhouse":
			roundhouseID = p.ID
		case "Merlin":
			merlinID = p.ID
		}
	}
	if len(ids) != 3 {
		t.Errorf("expected 3 distinct ids, got %d: %v", len(ids), ids)
	}
	if roundhouseID != "Bot|Roundhouse" {
		t.Errorf("Roundhouse id: want \"Bot|Roundhouse\", got %q", roundhouseID)
	}
	if merlinID != "Bot|Merlin" {
		t.Errorf("Merlin id: want \"Bot|Merlin\", got %q", merlinID)
	}
	// Real player should pass through untouched.
	for _, p := range got[0].Players {
		if p.Name == "s1gh" && p.ID != "Steam|111|0" {
			t.Errorf("real-player id mutated: got %q", p.ID)
		}
	}
	// Bots must have IsBot true (synthetic events read this field).
	for _, p := range got[0].Players {
		if (p.Name == "Roundhouse" || p.Name == "Merlin") && !p.IsBot {
			t.Errorf("%s should have IsBot=true", p.Name)
		}
		if p.Name == "s1gh" && p.IsBot {
			t.Errorf("s1gh should have IsBot=false")
		}
	}
}

// TestRewriteUpdateStateBotIds covers the dispatcher-level wire rewriter.
// Every downstream raw-bus subscriber depends on this — without it,
// SDK-side match.build (which reads raw UpdateState directly) would
// still see the Unknown|0|0 collision.
func TestRewriteUpdateStateBotIds(t *testing.T) {
	t.Run("rewrites bot ids in PascalCase wire", func(t *testing.T) {
		raw := envelope("UpdateState", map[string]any{
			"MatchGuid": "m",
			"Players": []map[string]any{
				{"PrimaryId": "Steam|111|0", "Name": "Alice", "TeamNum": 0},
				{"PrimaryId": "Unknown|0|0", "Name": "Roundhouse", "TeamNum": 1},
				{"PrimaryId": "Unknown|0|0", "Name": "Merlin", "TeamNum": 1},
			},
		})
		out := rewriteUpdateStateBotIds(raw)

		// Decode to verify.
		var env struct {
			Data string `json:"Data"`
		}
		if err := json.Unmarshal(out, &env); err != nil {
			t.Fatalf("envelope decode: %v", err)
		}
		var inner struct {
			Players []struct {
				PrimaryID string `json:"PrimaryId"`
				Name      string `json:"Name"`
			} `json:"Players"`
		}
		if err := json.Unmarshal([]byte(env.Data), &inner); err != nil {
			t.Fatalf("inner decode: %v", err)
		}
		want := map[string]string{
			"Alice":      "Steam|111|0",
			"Roundhouse": "Bot|Roundhouse",
			"Merlin":     "Bot|Merlin",
		}
		for _, p := range inner.Players {
			if got := want[p.Name]; p.PrimaryID != got {
				t.Errorf("%s: want id %q, got %q", p.Name, got, p.PrimaryID)
			}
		}
	})

	t.Run("returns original when no bot sentinel present", func(t *testing.T) {
		raw := envelope("UpdateState", map[string]any{
			"MatchGuid": "m",
			"Players": []map[string]any{
				{"PrimaryId": "Steam|111|0", "Name": "Alice", "TeamNum": 0},
				{"PrimaryId": "Steam|222|0", "Name": "Bob", "TeamNum": 1},
			},
		})
		out := rewriteUpdateStateBotIds(raw)
		// Same backing array — fast path returned input unchanged.
		if &raw[0] != &out[0] {
			t.Errorf("expected fast-path passthrough (same backing array)")
		}
	})

	t.Run("preserves bot id when name missing", func(t *testing.T) {
		raw := envelope("UpdateState", map[string]any{
			"MatchGuid": "m",
			"Players": []map[string]any{
				{"PrimaryId": "Unknown|0|0", "Name": "", "TeamNum": 1},
			},
		})
		out := rewriteUpdateStateBotIds(raw)
		// No rewrite happened — fast path or not, the wire shouldn't
		// contain a "Bot|" id since we have no name to mint from.
		if bytes.Contains(out, []byte("Bot|")) {
			t.Errorf("unexpected Bot| id minted from empty name: %s", out)
		}
	})

	t.Run("malformed envelope passes through unchanged", func(t *testing.T) {
		raw := []byte("not json at all but contains Unknown|0|0 substring")
		out := rewriteUpdateStateBotIds(raw)
		if !bytes.Equal(raw, out) {
			t.Errorf("malformed input should pass through; got %q", out)
		}
	})

	// Regression: the original implementation re-marshaled the outer
	// envelope through map[string]any, which sorts keys alphabetically.
	// A rewritten UpdateState then started with `{"Data":"..."`, and
	// because Data carries the full per-tick payload (multi-KB), the
	// "Event" key landed past extractEventName's 96-byte scan window,
	// returning "" — so Bus.Broadcast dropped the packet for every
	// filtered subscriber. That manifested as widgets showing briefly
	// then vanishing the moment a bot match started.
	t.Run("preserves outer envelope so extractEventName still works", func(t *testing.T) {
		// Pad the inner payload so the Event key would be way past the
		// 96-byte window if the rewriter reordered the outer envelope.
		players := []map[string]any{
			{"PrimaryId": "Unknown|0|0", "Name": "Roundhouse", "TeamNum": 1},
		}
		// 50 lookalike fields to fatten the inner payload past 96 bytes.
		extras := map[string]any{}
		for i := 0; i < 50; i++ {
			extras[strings.Repeat("x", 4)+strconv.Itoa(i)] = "filler-value-filler-value-filler-value"
		}
		inner := map[string]any{
			"MatchGuid": "m",
			"Players":   players,
			"Extras":    extras,
		}
		raw := envelope("UpdateState", inner)
		out := rewriteUpdateStateBotIds(raw)
		if name := extractEventName(out); name != "UpdateState" {
			t.Fatalf("extractEventName after rewrite = %q, want %q (rewrite broke envelope ordering)", name, "UpdateState")
		}
		// And the inner payload is still well-formed and rewritten.
		var env struct {
			Data string `json:"Data"`
		}
		if err := json.Unmarshal(out, &env); err != nil {
			t.Fatalf("envelope decode: %v", err)
		}
		if !strings.Contains(env.Data, `"PrimaryId":"Bot|Roundhouse"`) {
			t.Errorf("inner Data missing rewritten bot id: %s", env.Data)
		}
	})
}

// TestCanonicalizeBotId covers the boundary helper directly.
func TestCanonicalizeBotId(t *testing.T) {
	cases := []struct {
		name     string
		inID     string
		inName   string
		want     string
	}{
		{"real player passthrough", "Steam|111|0", "Alice", "Steam|111|0"},
		{"real player named Bot still passes", "Steam|111|0", "Bot", "Steam|111|0"},
		{"bot with name → minted", "Unknown|0|0", "Roundhouse", "Bot|Roundhouse"},
		{"bot without name → preserved", "Unknown|0|0", "", "Unknown|0|0"},
		{"bot name with pipe sanitized", "Unknown|0|0", "Round|house", "Bot|Round_house"},
		{"empty id passthrough", "", "Whatever", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := canonicalizeBotId(c.inID, c.inName); got != c.want {
				t.Errorf("canonicalizeBotId(%q, %q) = %q, want %q", c.inID, c.inName, got, c.want)
			}
		})
	}
}

func TestRosterTracker_DoesntStallOnBus(t *testing.T) {
	// Sanity check — feeding many packets should never block. The
	// underlying bus drops slow subscribers rather than backpressure.
	bus := NewBus()
	tracker := NewRosterTracker(bus)
	// No subscriber attached — Broadcast should just no-op for missing
	// receivers.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			feedRaw(t, tracker, bus, envelope("UpdateState", map[string]any{
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
