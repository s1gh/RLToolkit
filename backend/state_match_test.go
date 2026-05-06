package main

import (
	"testing"
	"time"
)

func TestMatchState_InitialSnapshot(t *testing.T) {
	ms := NewMatchState()
	snap := ms.Snapshot()
	if snap.MatchActive {
		t.Error("expected matchActive=false at init")
	}
	if snap.Phase != PhasePhaseNone {
		t.Errorf("expected phase=none at init, got %q", snap.Phase)
	}
	if snap.PreviousPhase != PhasePhaseNone {
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
