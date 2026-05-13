package state

import (
	"rl-toolkit/backend/internal/bus"
	"rl-toolkit/backend/internal/types"
	"testing"
	"time"
)

func TestMatchState_InitialSnapshot(t *testing.T) {
	ms := New()
	snap := ms.Snapshot()
	if snap.MatchActive {
		t.Error("expected matchActive=false at init")
	}
	if snap.Phase != types.PhaseNone {
		t.Errorf("expected phase=none at init, got %q", snap.Phase)
	}
	if snap.PreviousPhase != types.PhaseNone {
		t.Errorf("expected previousPhase=none at init, got %q", snap.PreviousPhase)
	}
	if snap.Trigger != "initial" {
		t.Errorf("expected trigger=initial at init, got %q", snap.Trigger)
	}
	if snap.Since.IsZero() {
		t.Error("expected since to be set at init")
	}
	if time.Since(snap.Since) > time.Second {
		t.Errorf("since too far in the past: %v", snap.Since)
	}
}

func TestMatchState_TransitionsThroughBasicMatch(t *testing.T) {
	ms := New()

	feed := func(name, data string) []bus.Event {
		evt := bus.Event{Name: name, Data: []byte(data), Raw: []byte(`{"Event":"` + name + `","Data":` + data + `}`)}
		ms.Observe(evt)
		return ms.Process(evt)
	}

	cases := []struct {
		name    string
		data    string
		want    types.Phase
		emit    bool
		trigger string
	}{
		{"MatchCreated", `"{\"MatchGuid\":\"abc\"}"`, types.PhaseLobby, true, "MatchCreated"},
		{"CountdownBegin", `""`, types.PhaseCountdown, true, "CountdownBegin"},
		{"RoundStarted", `""`, types.PhaseLive, true, "RoundStarted"},
		{"MatchPaused", `""`, types.PhasePaused, true, "MatchPaused"},
		{"MatchUnpaused", `""`, types.PhaseLive, true, "MatchUnpaused"},
		{"MatchEnded", `""`, types.PhaseEnded, true, "MatchEnded"},
		{"PodiumStart", `""`, types.PhasePodium, true, "PodiumStart"},
		{"MatchDestroyed", `""`, types.PhaseNone, true, "MatchDestroyed"},
	}

	for _, c := range cases {
		emitted := feed(c.name, c.data)
		snap := ms.Snapshot()
		if snap.Phase != c.want {
			t.Fatalf("after %s: phase=%q, want %q", c.name, snap.Phase, c.want)
		}
		if c.emit && len(emitted) == 0 {
			t.Fatalf("after %s: expected at least one emission, got 0", c.name)
		}
		if c.emit && emitted[0].Name != "_MatchState" {
			t.Fatalf("after %s: emission[0] name=%q, want _MatchState", c.name, emitted[0].Name)
		}
	}
}

// _MatchStarted fires exactly once per matchGuid, on the first
// transition into a live-ish phase (Countdown or Live).
func TestMatchState_MatchStartedFiresOncePerGuid(t *testing.T) {
	ms := New()
	feed := func(name, data string) []bus.Event {
		evt := bus.Event{Name: name, Data: []byte(data)}
		ms.Observe(evt)
		return ms.Process(evt)
	}

	// MatchCreated stamps the guid but doesn't enter a live-ish phase.
	out := feed("MatchCreated", `"{\"MatchGuid\":\"match-1\"}"`)
	for _, e := range out {
		if e.Name == "_MatchStarted" {
			t.Fatalf("_MatchStarted must not fire on lobby phase: %+v", out)
		}
	}

	// CountdownBegin transitions into the live-ish Countdown phase
	// with guid carried over. _MatchStarted should fire.
	out = feed("CountdownBegin", `""`)
	started := false
	for _, e := range out {
		if e.Name == "_MatchStarted" {
			started = true
			if !contains(string(e.Data), "match-1") {
				t.Errorf("payload missing guid: %s", string(e.Data))
			}
		}
	}
	if !started {
		t.Fatalf("expected _MatchStarted on first CountdownBegin, got: %+v", out)
	}

	// RoundStarted transitions into Live with the same guid; no
	// re-emit because this isn't a new match.
	out = feed("RoundStarted", `""`)
	for _, e := range out {
		if e.Name == "_MatchStarted" {
			t.Fatalf("_MatchStarted re-fired for the same guid: %+v", out)
		}
	}

	// Goal kickoff: countdown phase again, same guid. No re-emit.
	out = feed("CountdownBegin", `""`)
	for _, e := range out {
		if e.Name == "_MatchStarted" {
			t.Fatalf("_MatchStarted re-fired for the same guid on next kickoff: %+v", out)
		}
	}

	// MatchDestroyed should clear lastStartedGuid so a replay of the
	// same guid would emit again.
	feed("MatchDestroyed", `""`)
	feed("MatchCreated", `"{\"MatchGuid\":\"match-1\"}"`)
	out = feed("CountdownBegin", `""`)
	again := false
	for _, e := range out {
		if e.Name == "_MatchStarted" {
			again = true
		}
	}
	if !again {
		t.Fatalf("MatchDestroyed should reset _MatchStarted gating, got: %+v", out)
	}
}

// _MatchStarted fires only on the first transition into a live-ish
// phase per guid — a different guid arriving later must re-emit.
func TestMatchState_MatchStartedFiresPerNewGuid(t *testing.T) {
	ms := New()
	feed := func(name, data string) []bus.Event {
		evt := bus.Event{Name: name, Data: []byte(data)}
		ms.Observe(evt)
		return ms.Process(evt)
	}

	feed("MatchCreated", `"{\"MatchGuid\":\"match-A\"}"`)
	feed("CountdownBegin", `""`) // _MatchStarted for match-A
	feed("MatchEnded", `""`)
	feed("MatchCreated", `"{\"MatchGuid\":\"match-B\"}"`)
	out := feed("CountdownBegin", `""`)

	for _, e := range out {
		if e.Name == "_MatchStarted" {
			if !contains(string(e.Data), "match-B") {
				t.Errorf("expected match-B in payload, got %s", string(e.Data))
			}
			return
		}
	}
	t.Fatalf("expected _MatchStarted for match-B, got: %+v", out)
}

// _MatchAbandoned fires when MatchDestroyed lands without a
// preceding MatchEnded for the same guid (user ragequit, connection
// lost mid-match, etc.). RL itself counts this as a forfeit-loss on
// the leaver's record; plugins use this event to mirror that.
func TestMatchState_MatchAbandonedFiresOnEarlyLeave(t *testing.T) {
	ms := New()
	feed := func(name, data string) []bus.Event {
		evt := bus.Event{Name: name, Data: []byte(data)}
		ms.Observe(evt)
		return ms.Process(evt)
	}

	feed("MatchCreated", `"{\"MatchGuid\":\"match-X\"}"`)
	feed("CountdownBegin", `""`)
	feed("RoundStarted", `""`)
	// No MatchEnded — user ragequits.
	out := feed("MatchDestroyed", `""`)

	abandoned := false
	for _, e := range out {
		if e.Name == "_MatchAbandoned" {
			abandoned = true
			if !contains(string(e.Data), "match-X") {
				t.Errorf("payload missing guid: %s", string(e.Data))
			}
		}
	}
	if !abandoned {
		t.Fatalf("expected _MatchAbandoned on early leave, got: %+v", out)
	}
}

// A clean MatchEnded followed by MatchDestroyed is normal teardown,
// not an abandon. _MatchAbandoned must NOT fire in that case.
func TestMatchState_MatchAbandonedSkipsCleanEnd(t *testing.T) {
	ms := New()
	feed := func(name, data string) []bus.Event {
		evt := bus.Event{Name: name, Data: []byte(data)}
		ms.Observe(evt)
		return ms.Process(evt)
	}

	feed("MatchCreated", `"{\"MatchGuid\":\"match-Y\"}"`)
	feed("CountdownBegin", `""`)
	feed("RoundStarted", `""`)
	feed("MatchEnded", `""`)
	out := feed("MatchDestroyed", `""`)

	for _, e := range out {
		if e.Name == "_MatchAbandoned" {
			t.Fatalf("_MatchAbandoned must not fire after a clean MatchEnded: %+v", out)
		}
	}
}

// MatchDestroyed before any match was started (lobby teardown,
// reconnect noise) has no guid to fire for and must be silent.
func TestMatchState_MatchAbandonedSilentWithoutStart(t *testing.T) {
	ms := New()
	feed := func(name, data string) []bus.Event {
		evt := bus.Event{Name: name, Data: []byte(data)}
		ms.Observe(evt)
		return ms.Process(evt)
	}

	out := feed("MatchDestroyed", `""`)
	for _, e := range out {
		if e.Name == "_MatchAbandoned" {
			t.Fatalf("_MatchAbandoned must not fire without a prior _MatchStarted: %+v", out)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestMatchState_NoEmissionOnIdentityTransition(t *testing.T) {
	ms := New()
	feed := func(name string) []bus.Event {
		evt := bus.Event{Name: name, Data: []byte(`""`)}
		ms.Observe(evt)
		return ms.Process(evt)
	}

	if got := feed("MatchCreated"); len(got) == 0 {
		t.Fatal("expected emission on first MatchCreated")
	}
	if got := feed("MatchCreated"); len(got) != 0 {
		t.Fatalf("expected no emission on identity transition, got %d", len(got))
	}
}

func TestMatchState_WatchdogFlipsInactive(t *testing.T) {
	ms := New()
	ms.timeout = 50 * time.Millisecond

	// Drive into a live state via UpdateState.
	evt := bus.Event{Name: "UpdateState", Raw: []byte(`{"Event":"UpdateState","Data":"{\"MatchGuid\":\"abc\"}"}`)}
	ms.Observe(evt)
	_ = ms.Process(evt)

	if !ms.Snapshot().MatchActive {
		t.Fatal("expected matchActive=true after UpdateState")
	}

	// Wait past the timeout, then check.
	time.Sleep(120 * time.Millisecond)
	ms.checkTimeout()

	emitted := ms.Process(bus.Event{Name: "tick"})
	if len(emitted) != 1 {
		t.Fatalf("expected 1 _MatchState emission from watchdog, got %d", len(emitted))
	}
	snap := ms.Snapshot()
	if snap.MatchActive {
		t.Error("expected matchActive=false after watchdog")
	}
	if snap.Phase != types.PhaseNone {
		t.Errorf("expected phase=none after watchdog, got %q", snap.Phase)
	}
	if snap.Trigger != "watchdogTimeout" {
		t.Errorf("expected trigger=watchdogTimeout, got %q", snap.Trigger)
	}
}
