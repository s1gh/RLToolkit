package state

import (
	"rl-toolkit/internal/bus"
	"rl-toolkit/internal/types"
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
		if c.emit && len(emitted) != 1 {
			t.Fatalf("after %s: expected 1 _MatchState emission, got %d", c.name, len(emitted))
		}
		if c.emit && emitted[0].Name != "_MatchState" {
			t.Fatalf("after %s: emission name=%q, want _MatchState", c.name, emitted[0].Name)
		}
	}
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
