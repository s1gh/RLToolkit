package main

import "testing"

// TestSynthesizer_StatfeedEvent_Demolish replays a fixture where Alice
// demolishes Bob and asserts that the synthesizer publishes a
// _StatfeedEvent with both targets resolved against the live roster.
func TestSynthesizer_StatfeedEvent_Demolish(t *testing.T) {
	bus := NewEventBus()
	roster := NewRosterTracker(bus)
	synth := NewSynthesizer(bus, roster)

	got := captureSynthetic(t, bus, nil, roster, nil, synth, "testdata/fixtures/statfeed_demo.jsonl", "_StatfeedEvent")

	if len(got) != 1 {
		t.Fatalf("expected exactly 1 _StatfeedEvent, got %d", len(got))
	}
	ev := got[0]

	if ev["eventName"] != "Demolish" {
		t.Errorf("eventName: want Demolish, got %v", ev["eventName"])
	}
	if ev["matchGuid"] != "demo-match" {
		t.Errorf("matchGuid: want demo-match, got %v", ev["matchGuid"])
	}

	main, ok := ev["mainTarget"].(map[string]interface{})
	if !ok {
		t.Fatalf("mainTarget missing or wrong type: %T", ev["mainTarget"])
	}
	if main["id"] != "Steam|111|0" {
		t.Errorf("mainTarget.id: want Steam|111|0, got %v", main["id"])
	}
	if main["name"] != "Alice" {
		t.Errorf("mainTarget.name: want Alice, got %v", main["name"])
	}
	if main["platform"] != "Steam" {
		t.Errorf("mainTarget.platform: want Steam, got %v", main["platform"])
	}
	// team: JSON numbers decode as float64
	if main["team"].(float64) != 0 {
		t.Errorf("mainTarget.team: want 0, got %v", main["team"])
	}

	secondary, ok := ev["secondaryTarget"].(map[string]interface{})
	if !ok {
		t.Fatalf("secondaryTarget missing or wrong type: %T", ev["secondaryTarget"])
	}
	if secondary["id"] != "Steam|222|0" {
		t.Errorf("secondaryTarget.id: want Steam|222|0, got %v", secondary["id"])
	}
	if secondary["name"] != "Bob" {
		t.Errorf("secondaryTarget.name: want Bob, got %v", secondary["name"])
	}
}

// TestSynthesizer_StatfeedEvent_NoSecondaryTarget verifies that a Statfeed
// with only a MainTarget (e.g., Save, Shot, FlipReset) produces an
// enriched event with secondaryTarget omitted.
func TestSynthesizer_StatfeedEvent_NoSecondaryTarget(t *testing.T) {
	bus := NewEventBus()
	roster := NewRosterTracker(bus)
	synth := NewSynthesizer(bus, roster)

	// Seed the roster.
	roster.Feed([]byte(`{"Event":"UpdateState","Data":"{\"MatchGuid\":\"m\",\"Players\":[{\"PrimaryId\":\"Steam|111|0\",\"Name\":\"Alice\",\"TeamNum\":0}]}"}`))

	ch, cancel := bus.Subscribe(nil)
	defer cancel()

	synth.Feed([]byte(`{"Event":"StatfeedEvent","Data":"{\"MatchGuid\":\"m\",\"EventName\":\"Save\",\"Type\":\"Normal\",\"MainTarget\":{\"Name\":\"Alice\",\"Shortcut\":\"Alice\",\"TeamNum\":0}}"}`))

	// Drain.
	var got []byte
	select {
	case raw := <-ch:
		got = raw
	default:
	}
	if got == nil {
		// Try once more — the synth publishes asynchronously through the bus
		// internals; give it a microtask.
		select {
		case raw := <-ch:
			got = raw
		default:
			t.Fatal("synth did not publish a _StatfeedEvent")
		}
	}

	// Assert the JSON omits secondaryTarget.
	if !containsBytes(got, []byte(`"_StatfeedEvent"`)) {
		t.Fatalf("expected _StatfeedEvent in payload, got: %s", got)
	}
	if containsBytes(got, []byte(`"secondaryTarget"`)) {
		t.Fatalf("expected secondaryTarget to be omitted, got: %s", got)
	}
}

// TestSynthesizer_BallHit replays the basic_match fixture and asserts
// that the BallHit row produces a _BallHit with Alice resolved against
// the roster.
func TestSynthesizer_BallHit(t *testing.T) {
	bus := NewEventBus()
	roster := NewRosterTracker(bus)
	synth := NewSynthesizer(bus, roster)

	got := captureSynthetic(t, bus, nil, roster, nil, synth, "testdata/fixtures/basic_match.jsonl", "_BallHit")

	if len(got) != 1 {
		t.Fatalf("expected exactly 1 _BallHit, got %d", len(got))
	}
	ev := got[0]

	if ev["matchGuid"] != "match-abc-123" {
		t.Errorf("matchGuid: want match-abc-123, got %v", ev["matchGuid"])
	}
	players, ok := ev["players"].([]interface{})
	if !ok {
		t.Fatalf("players missing or wrong type: %T", ev["players"])
	}
	if len(players) != 1 {
		t.Fatalf("players: want 1, got %d", len(players))
	}
	p, ok := players[0].(map[string]interface{})
	if !ok {
		t.Fatalf("players[0] wrong type: %T", players[0])
	}
	if p["id"] != "Steam|111|0" {
		t.Errorf("players[0].id: want Steam|111|0, got %v", p["id"])
	}
	if p["name"] != "Alice" {
		t.Errorf("players[0].name: want Alice, got %v", p["name"])
	}
	// Speed/location should round-trip from the wire payload.
	if got, want := ev["postHitSpeed"].(float64), 1500.0; got != want {
		t.Errorf("postHitSpeed: want %v, got %v", want, got)
	}
}

// TestSynthesizer_CrossbarHit replays a one-shot fixture and asserts
// that BallLastTouch.Player is resolved against the roster.
func TestSynthesizer_CrossbarHit(t *testing.T) {
	bus := NewEventBus()
	roster := NewRosterTracker(bus)
	synth := NewSynthesizer(bus, roster)

	got := captureSynthetic(t, bus, nil, roster, nil, synth, "testdata/fixtures/crossbar_hit.jsonl", "_CrossbarHit")

	if len(got) != 1 {
		t.Fatalf("expected exactly 1 _CrossbarHit, got %d", len(got))
	}
	ev := got[0]

	if got, want := ev["ballSpeed"].(float64), 2300.0; got != want {
		t.Errorf("ballSpeed: want %v, got %v", want, got)
	}
	if got, want := ev["impactForce"].(float64), 12000.0; got != want {
		t.Errorf("impactForce: want %v, got %v", want, got)
	}

	lt, ok := ev["ballLastTouch"].(map[string]interface{})
	if !ok {
		t.Fatalf("ballLastTouch missing or wrong type: %T", ev["ballLastTouch"])
	}
	p, ok := lt["player"].(map[string]interface{})
	if !ok {
		t.Fatalf("ballLastTouch.player wrong type: %T", lt["player"])
	}
	if p["id"] != "Steam|111|0" {
		t.Errorf("ballLastTouch.player.id: want Steam|111|0, got %v", p["id"])
	}
	if p["name"] != "Alice" {
		t.Errorf("ballLastTouch.player.name: want Alice, got %v", p["name"])
	}
}

func containsBytes(haystack, needle []byte) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		match := true
		for j := 0; j < len(needle); j++ {
			if haystack[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
