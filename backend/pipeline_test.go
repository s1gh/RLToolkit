package main

import (
	"context"
	"sync"
	"testing"
)

// staticSource produces a fixed list of events then closes.
type staticSource struct {
	events []Event
}

func (s *staticSource) Events(ctx context.Context) <-chan Event {
	ch := make(chan Event, len(s.events))
	for _, e := range s.events {
		ch <- e
	}
	close(ch)
	return ch
}

// recordingBroadcaster captures every Broadcast call.
type recordingBroadcaster struct {
	mu     sync.Mutex
	events []Event
}

func (b *recordingBroadcaster) Broadcast(evt Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.events = append(b.events, evt)
}

func (b *recordingBroadcaster) snapshot() []Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]Event, len(b.events))
	copy(out, b.events)
	return out
}

// recordingState observes events into a slice.
type recordingState struct {
	observed []string
}

func (r *recordingState) Observe(evt Event) { r.observed = append(r.observed, evt.Name) }

// echoEmit emits one synthetic event named "_Echo:<input>" for every event.
type echoEmit struct{}

func (e *echoEmit) Process(evt Event) []Event {
	return []Event{{Name: "_Echo:" + evt.Name}}
}

func TestPipeline_RunsStateBeforeEmit(t *testing.T) {
	src := &staticSource{events: []Event{{Name: "A"}, {Name: "B"}}}
	dst := &recordingBroadcaster{}
	state := &recordingState{}
	emit := &echoEmit{}

	p := NewPipeline()
	p.AddState(state)
	p.AddEmit(emit)

	p.Run(context.Background(), src, dst)

	if got, want := state.observed, []string{"A", "B"}; !equalStrings(got, want) {
		t.Fatalf("state observations: got %v, want %v", got, want)
	}

	got := dst.snapshot()
	wantNames := []string{"A", "_Echo:A", "B", "_Echo:B"}
	if len(got) != len(wantNames) {
		t.Fatalf("broadcasts: got %d events, want %d (%v)", len(got), len(wantNames), got)
	}
	for i, n := range wantNames {
		if got[i].Name != n {
			t.Fatalf("broadcast[%d]: got %q, want %q", i, got[i].Name, n)
		}
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
