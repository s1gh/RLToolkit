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

// TestSynthesizer_GoalScored replays the basic_match fixture and asserts
// that the GoalScored row produces a _GoalScored with Scorer resolved,
// scoringTeam=0, concedingTeam=1, and isOwnGoal=false.
func TestSynthesizer_GoalScored(t *testing.T) {
	bus := NewEventBus()
	roster := NewRosterTracker(bus)
	synth := NewSynthesizer(bus, roster)

	got := captureSynthetic(t, bus, nil, roster, nil, synth, "testdata/fixtures/basic_match.jsonl", "_GoalScored")

	if len(got) != 1 {
		t.Fatalf("expected exactly 1 _GoalScored, got %d", len(got))
	}
	ev := got[0]

	scorer, ok := ev["scorer"].(map[string]interface{})
	if !ok {
		t.Fatalf("scorer missing or wrong type: %T", ev["scorer"])
	}
	if scorer["id"] != "Steam|111|0" {
		t.Errorf("scorer.id: want Steam|111|0, got %v", scorer["id"])
	}
	if got, want := ev["scoringTeam"].(float64), 0.0; got != want {
		t.Errorf("scoringTeam: want %v, got %v", want, got)
	}
	if got, want := ev["concedingTeam"].(float64), 1.0; got != want {
		t.Errorf("concedingTeam: want %v, got %v", want, got)
	}
	if isOwn, _ := ev["isOwnGoal"].(bool); isOwn {
		t.Errorf("isOwnGoal: want false, got true")
	}
	if _, hasAssister := ev["assister"]; hasAssister {
		t.Errorf("assister: should be omitted for solo goal")
	}
}

// TestSynthesizer_GoalScored_OwnGoal asserts that a deflection by an
// opposing team member sets isOwnGoal on the synthesized event.
func TestSynthesizer_GoalScored_OwnGoal(t *testing.T) {
	bus := NewEventBus()
	roster := NewRosterTracker(bus)
	synth := NewSynthesizer(bus, roster)

	got := captureSynthetic(t, bus, nil, roster, nil, synth, "testdata/fixtures/own_goal.jsonl", "_GoalScored")

	if len(got) != 1 {
		t.Fatalf("expected exactly 1 _GoalScored, got %d", len(got))
	}
	ev := got[0]
	if isOwn, _ := ev["isOwnGoal"].(bool); !isOwn {
		t.Errorf("isOwnGoal: want true, got false")
	}
	lt, ok := ev["ballLastTouch"].(map[string]interface{})
	if !ok {
		t.Fatalf("ballLastTouch missing or wrong type: %T", ev["ballLastTouch"])
	}
	bp, ok := lt["player"].(map[string]interface{})
	if !ok {
		t.Fatalf("ballLastTouch.player wrong type: %T", lt["player"])
	}
	if bp["name"] != "Bob" {
		t.Errorf("ballLastTouch.player.name: want Bob, got %v", bp["name"])
	}
}

// TestSynthesizer_GoalScored_Modifiers asserts that same-player
// modifier statfeeds fired before a GoalScored land on the synthesized
// modifiers block. The aerial_goal fixture fires AerialGoal + LongGoal
// statfeeds for Alice, then a GoalScored by Alice — both flags should
// be set, others should not.
func TestSynthesizer_GoalScored_Modifiers(t *testing.T) {
	bus := NewEventBus()
	roster := NewRosterTracker(bus)
	synth := NewSynthesizer(bus, roster)

	got := captureSynthetic(t, bus, nil, roster, nil, synth, "testdata/fixtures/aerial_goal.jsonl", "_GoalScored")

	if len(got) != 1 {
		t.Fatalf("expected exactly 1 _GoalScored, got %d", len(got))
	}
	ev := got[0]
	mods, ok := ev["modifiers"].(map[string]interface{})
	if !ok {
		t.Fatalf("modifiers missing or wrong type: %T", ev["modifiers"])
	}
	if mods["isAerialGoal"] != true {
		t.Errorf("isAerialGoal: want true, got %v", mods["isAerialGoal"])
	}
	if mods["isLongGoal"] != true {
		t.Errorf("isLongGoal: want true, got %v", mods["isLongGoal"])
	}
	// Untriggered modifiers should be absent (omitempty).
	if _, has := mods["isHatTrickGoal"]; has {
		t.Errorf("isHatTrickGoal: should be omitted when false")
	}
	if _, has := mods["isBackwardsGoal"]; has {
		t.Errorf("isBackwardsGoal: should be omitted when false")
	}
}

// TestSynthesizer_OwnGoal asserts that a score delta against a recent
// BallHit by the opposing team produces an _OwnGoal envelope with the
// deflector resolved + scoreAfter populated.
func TestSynthesizer_OwnGoal(t *testing.T) {
	bus := NewEventBus()
	roster := NewRosterTracker(bus)
	pm := NewPhaseMachine(bus)
	synth := NewSynthesizer(bus, roster)
	synth.AttachPhaseMachine(pm)

	got := captureSynthetic(t, bus, nil, roster, pm, synth, "testdata/fixtures/own_goal_delta.jsonl", "_OwnGoal")

	if len(got) != 1 {
		t.Fatalf("expected exactly 1 _OwnGoal, got %d", len(got))
	}
	ev := got[0]

	if got, want := ev["scoringTeam"].(float64), 0.0; got != want {
		t.Errorf("scoringTeam: want %v, got %v", want, got)
	}
	if got, want := ev["concedingTeam"].(float64), 1.0; got != want {
		t.Errorf("concedingTeam: want %v, got %v", want, got)
	}
	defl, ok := ev["deflector"].(map[string]interface{})
	if !ok {
		t.Fatalf("deflector missing or wrong type: %T", ev["deflector"])
	}
	if defl["name"] != "Bob" {
		t.Errorf("deflector.name: want Bob, got %v", defl["name"])
	}
	score, ok := ev["scoreAfter"].(map[string]interface{})
	if !ok {
		t.Fatalf("scoreAfter missing: %T", ev["scoreAfter"])
	}
	if score["blue"].(float64) != 1.0 {
		t.Errorf("scoreAfter.blue: want 1, got %v", score["blue"])
	}
	if score["orange"].(float64) != 0.0 {
		t.Errorf("scoreAfter.orange: want 0, got %v", score["orange"])
	}
}

// TestSynthesizer_OwnGoal_PhaseGate asserts that a score delta during a
// non-gameplay phase (e.g. lobby — no RoundStarted) does not produce
// an _OwnGoal even when a deflection fingerprint is present. This
// guards against forfeit/mercy-rule false positives.
func TestSynthesizer_OwnGoal_PhaseGate(t *testing.T) {
	bus := NewEventBus()
	roster := NewRosterTracker(bus)
	pm := NewPhaseMachine(bus)
	synth := NewSynthesizer(bus, roster)
	synth.AttachPhaseMachine(pm)

	// Same fixture as TestSynthesizer_OwnGoal but with RoundStarted
	// stripped, so the phase machine stays in lobby.
	feed := func(raw []byte) {
		roster.Feed(raw)
		pm.Feed(raw)
		bus.Publish(raw)
		synth.Feed(raw)
	}

	ch, cancel := bus.Subscribe(nil)
	defer cancel()

	feed([]byte(`{"Event":"MatchCreated","Data":"{\"MatchGuid\":\"x\"}"}`))
	feed([]byte(`{"Event":"UpdateState","Data":"{\"MatchGuid\":\"x\",\"Players\":[{\"PrimaryId\":\"Steam|111|0\",\"Name\":\"Alice\",\"TeamNum\":0},{\"PrimaryId\":\"Steam|222|0\",\"Name\":\"Bob\",\"TeamNum\":1}],\"Game\":{\"Teams\":[{\"TeamNum\":0,\"Name\":\"Blue\",\"Score\":0},{\"TeamNum\":1,\"Name\":\"Orange\",\"Score\":0}]}}"}`))
	feed([]byte(`{"Event":"BallHit","Data":"{\"MatchGuid\":\"x\",\"Players\":[{\"Name\":\"Bob\",\"Shortcut\":\"Bob\",\"TeamNum\":1}],\"Ball\":{\"PreHitSpeed\":1000,\"PostHitSpeed\":1500}}"}`))
	feed([]byte(`{"Event":"UpdateState","Data":"{\"MatchGuid\":\"x\",\"Players\":[{\"PrimaryId\":\"Steam|111|0\",\"Name\":\"Alice\",\"TeamNum\":0},{\"PrimaryId\":\"Steam|222|0\",\"Name\":\"Bob\",\"TeamNum\":1}],\"Game\":{\"Teams\":[{\"TeamNum\":0,\"Name\":\"Blue\",\"Score\":1},{\"TeamNum\":1,\"Name\":\"Orange\",\"Score\":0}]}}"}`))

	// Drain the bus and assert no _OwnGoal landed.
	for {
		select {
		case raw := <-ch:
			if name := extractEventName(raw); name == "_OwnGoal" {
				t.Fatalf("expected no _OwnGoal in lobby phase, got: %s", raw)
			}
		default:
			return
		}
	}
}

// TestSynthesizer_GoalScored_DropsEmptyScorer mirrors the SDK guard:
// GoalScored with empty Scorer.Name + Shortcut is dropped (RL re-fires
// at round-restart with empty Scorer).
func TestSynthesizer_GoalScored_DropsEmptyScorer(t *testing.T) {
	bus := NewEventBus()
	roster := NewRosterTracker(bus)
	synth := NewSynthesizer(bus, roster)

	ch, cancel := bus.Subscribe(nil)
	defer cancel()

	synth.Feed([]byte(`{"Event":"GoalScored","Data":"{\"MatchGuid\":\"x\",\"Scorer\":{\"Name\":\"\",\"Shortcut\":\"\",\"TeamNum\":0}}"}`))

	select {
	case raw := <-ch:
		t.Fatalf("expected no synthetic event, got: %s", raw)
	default:
		// pass
	}
}

// TestSynthesizer_MatchEnded replays the basic_match fixture and asserts
// that _MatchEnded carries WinnerTeamNum + winnerName + final scores.
func TestSynthesizer_MatchEnded(t *testing.T) {
	bus := NewEventBus()
	roster := NewRosterTracker(bus)
	synth := NewSynthesizer(bus, roster)

	got := captureSynthetic(t, bus, nil, roster, nil, synth, "testdata/fixtures/basic_match.jsonl", "_MatchEnded")

	if len(got) != 1 {
		t.Fatalf("expected exactly 1 _MatchEnded, got %d", len(got))
	}
	ev := got[0]

	if got, want := ev["winnerTeamNum"].(float64), 0.0; got != want {
		t.Errorf("winnerTeamNum: want %v, got %v", want, got)
	}
	if ev["winnerName"] != "Blue Team" {
		t.Errorf("winnerName: want \"Blue Team\", got %v", ev["winnerName"])
	}
	// The fixture's final UpdateState has scoreBlue=1, scoreOrange=0.
	if got, want := ev["scoreBlue"].(float64), 1.0; got != want {
		t.Errorf("scoreBlue: want %v, got %v", want, got)
	}
	if got, want := ev["scoreOrange"].(float64), 0.0; got != want {
		t.Errorf("scoreOrange: want %v, got %v", want, got)
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

// TestSynthesizer_PlayerDemolished asserts that a Demolish StatfeedEvent
// produces both the catch-all _StatfeedEvent and a dedicated
// _PlayerDemolished envelope with attacker/victim resolved + the
// isSelfDemo / isTeamDemo flags.
func TestSynthesizer_PlayerDemolished(t *testing.T) {
	bus := NewEventBus()
	roster := NewRosterTracker(bus)
	synth := NewSynthesizer(bus, roster)

	got := captureSynthetic(t, bus, nil, roster, nil, synth, "testdata/fixtures/statfeed_demo.jsonl", "_PlayerDemolished")

	if len(got) != 1 {
		t.Fatalf("expected exactly 1 _PlayerDemolished, got %d", len(got))
	}
	ev := got[0]

	att, ok := ev["attacker"].(map[string]interface{})
	if !ok {
		t.Fatalf("attacker missing or wrong type: %T", ev["attacker"])
	}
	if att["name"] != "Alice" {
		t.Errorf("attacker.name: want Alice, got %v", att["name"])
	}
	vic, ok := ev["victim"].(map[string]interface{})
	if !ok {
		t.Fatalf("victim missing or wrong type: %T", ev["victim"])
	}
	if vic["name"] != "Bob" {
		t.Errorf("victim.name: want Bob, got %v", vic["name"])
	}
	// Different teams → neither flag should be set.
	if _, has := ev["isSelfDemo"]; has {
		t.Errorf("isSelfDemo should be omitted for cross-team demo")
	}
	if _, has := ev["isTeamDemo"]; has {
		t.Errorf("isTeamDemo should be omitted for cross-team demo")
	}
}

// TestSynthesizer_NonFramingSyntheticsAreFilterable asserts that a
// subscriber with an explicit filter does NOT receive synthetic events
// it didn't subscribe to. Framing signals (_Lifecycle, _RosterChanged,
// etc.) keep bypassing.
func TestSynthesizer_NonFramingSyntheticsAreFilterable(t *testing.T) {
	bus := NewEventBus()
	roster := NewRosterTracker(bus)
	synth := NewSynthesizer(bus, roster)

	// Subscriber asks ONLY for _PlayerDemolished — it should not receive
	// the catch-all _StatfeedEvent on a Demolish.
	filter := map[string]struct{}{"_PlayerDemolished": {}}
	ch, cancel := bus.Subscribe(filter)
	defer cancel()

	// Seed roster.
	roster.Feed([]byte(`{"Event":"UpdateState","Data":"{\"MatchGuid\":\"m\",\"Players\":[{\"PrimaryId\":\"Steam|111|0\",\"Name\":\"Alice\",\"TeamNum\":0},{\"PrimaryId\":\"Steam|222|0\",\"Name\":\"Bob\",\"TeamNum\":1}]}"}`))
	// Drain the seed publish.
	for {
		select {
		case <-ch:
		default:
			goto seedDrained
		}
	}
seedDrained:

	// Synth fires both _StatfeedEvent and _PlayerDemolished.
	synth.Feed([]byte(`{"Event":"StatfeedEvent","Data":"{\"MatchGuid\":\"m\",\"EventName\":\"Demolish\",\"MainTarget\":{\"Name\":\"Alice\",\"Shortcut\":\"Alice\",\"TeamNum\":0},\"SecondaryTarget\":{\"Name\":\"Bob\",\"Shortcut\":\"Bob\",\"TeamNum\":1}}"}`))

	// Drain everything available; we should see _PlayerDemolished only.
	var events []string
	for {
		select {
		case raw := <-ch:
			events = append(events, extractEventName(raw))
		default:
			goto done
		}
	}
done:

	hasDemo := false
	for _, e := range events {
		if e == "_StatfeedEvent" {
			t.Errorf("filtered subscriber should NOT receive _StatfeedEvent")
		}
		if e == "_PlayerDemolished" {
			hasDemo = true
		}
	}
	if !hasDemo {
		t.Errorf("expected _PlayerDemolished, got events: %v", events)
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
