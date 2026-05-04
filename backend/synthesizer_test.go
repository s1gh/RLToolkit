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
