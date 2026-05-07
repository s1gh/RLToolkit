package backend

import (
	"bufio"
	"encoding/json"
	"os"
	"rl-toolkit/internal/bus"
	"testing"
	"time"
)

// fixtureReplay reads a JSON-lines fixture (one envelope per line) and
// feeds each line to the provided feeder function in order. Used by the
// synthetic-event test harness to replay real wire traffic through the
// backend without running Rocket League.
func fixtureReplay(t *testing.T, path string, feeder func([]byte)) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open fixture %q: %v", path, err)
	}
	defer f.Close()

	scan := bufio.NewScanner(f)
	lineNum := 0
	for scan.Scan() {
		lineNum++
		line := scan.Bytes()
		if len(line) == 0 {
			continue
		}
		// Sanity check: valid JSON envelope.
		var raw map[string]interface{}
		if err := json.Unmarshal(line, &raw); err != nil {
			t.Fatalf("fixture %s:%d: invalid JSON: %v", path, lineNum, err)
		}
		feeder(line)
	}
	if err := scan.Err(); err != nil {
		t.Fatalf("fixture %s: read error: %v", path, err)
	}
}

// captureSynthetic replays a fixture through the given bus and returns
// every synthetic event whose name matches `eventName`. The fixture is
// fed to MatchState (Observe-then-Process) and the roster tracker
// before being published, preserving the same ordering as the real
// dispatcher.
//
// If eventName is empty, all synthetic events (those starting with "_")
// are returned.
func captureSynthetic(t *testing.T, b *bus.Bus, matchState *MatchState, roster *RosterTracker, fixturePath string, eventName string) []map[string]interface{} {
	t.Helper()

	ch, cancel := b.Subscribe(nil)
	defer cancel()

	feed := func(raw []byte) {
		name := extractEventName(raw)
		var env struct {
			Data      json.RawMessage `json:"Data,omitempty"`
			DataLower json.RawMessage `json:"data,omitempty"`
		}
		_ = json.Unmarshal(raw, &env)
		data := env.Data
		if len(data) == 0 {
			data = env.DataLower
		}
		evt := bus.Event{Name: name, Data: data, Raw: raw}
		if roster != nil {
			roster.Observe(evt)
		}
		if matchState != nil {
			matchState.Observe(evt)
		}
		b.Broadcast(evt)
		if matchState != nil {
			for _, out := range matchState.Process(evt) {
				b.Broadcast(out)
			}
		}
		if roster != nil {
			for _, out := range roster.Process(evt) {
				b.Broadcast(out)
			}
		}
	}

	fixtureReplay(t, fixturePath, feed)

	// Drain the subscriber channel with a generous deadline so that
	// synthetic events published inline have time to arrive.
	deadline := time.NewTimer(50 * time.Millisecond)
	defer deadline.Stop()

	var out []map[string]interface{}
	done := false
	for !done {
		select {
		case raw, ok := <-ch:
			if !ok {
				done = true
				break
			}
			name := extractEventName(raw)
			if len(name) == 0 || name[0] != '_' {
				continue
			}
			if eventName != "" && name != eventName {
				continue
			}
			var payload map[string]interface{}
			if err := json.Unmarshal(raw, &payload); err != nil {
				t.Fatalf("unmarshal synthetic event %q: %v", name, err)
			}
			out = append(out, payload)
		case <-deadline.C:
			done = true
		}
	}
	return out
}

// TestFixtureReplay_BasicMatch exercises the harness itself: replay the
// basic_match fixture and assert that the expected synthetic events are
// captured in the right order.
func TestFixtureReplay_BasicMatch(t *testing.T) {
	bus := bus.NewBus()
	matchState := NewMatchState()
	roster := NewRosterTracker(bus)

	got := captureSynthetic(t, bus, matchState, roster, "testdata/fixtures/basic_match.jsonl", "")

	// We expect at minimum: _MatchState transitions + _RosterChanged.
	// The exact count depends on the fixture, but we can sanity-check
	// ordering and presence.
	var matchStateCount, rosterCount int
	var phases []string
	for _, ev := range got {
		switch ev["Event"] {
		case "_MatchState":
			matchStateCount++
			data, _ := ev["Data"].(map[string]interface{})
			phase, _ := data["phase"].(string)
			if len(phases) == 0 || phases[len(phases)-1] != phase {
				phases = append(phases, phase)
			}
		case "_RosterChanged":
			rosterCount++
		}
	}

	if matchStateCount == 0 {
		t.Error("expected at least one _MatchState event")
	}
	if rosterCount == 0 {
		t.Error("expected at least one _RosterChanged event")
	}

	// Phase progression check: the fixture drives the match through the
	// canonical lifecycle. Verify the prefix of observed phase
	// transitions matches the expected sequence so regressions in the
	// state machine surface here.
	wantPhases := []string{"lobby", "countdown", "live"}
	if len(phases) < len(wantPhases) {
		t.Fatalf("expected at least phases %v, got %v", wantPhases, phases)
	}
	for i, want := range wantPhases {
		if phases[i] != want {
			t.Errorf("phase[%d] = %q, want %q (full sequence: %v)", i, phases[i], want, phases)
		}
	}
}
