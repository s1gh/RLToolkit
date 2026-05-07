package backend

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

// prefixEmit emits a single event named "<prefix>:<input>" for every
// event whose name doesn't already start with this emitter's prefix —
// the guard keeps the recursion finite when chained processors would
// otherwise feed each other forever.
type prefixEmit struct{ prefix string }

func (p *prefixEmit) Process(evt Event) []Event {
	if len(evt.Name) >= len(p.prefix) && evt.Name[:len(p.prefix)] == p.prefix {
		return nil
	}
	return []Event{{Name: p.prefix + ":" + evt.Name}}
}

// TestPipeline_EmissionsFeedDownstreamEmitters verifies that a later
// emitter sees the events produced by an earlier one. Order matters:
// `first` emits "_one:<x>", `second` emits "_two:<x>". For source "A"
// we expect:
//
//	A, _one:A, _two:A, _two:_one:A, _two:B, ...
//
// — because `first` runs on A, produces _one:A, then `second` runs on
// both A (sibling) and _one:A (downstream of first's output).
func TestPipeline_EmissionsFeedDownstreamEmitters(t *testing.T) {
	src := &staticSource{events: []Event{{Name: "A"}}}
	dst := &recordingBroadcaster{}
	first := &prefixEmit{prefix: "_one"}
	second := &prefixEmit{prefix: "_two"}

	p := NewPipeline()
	p.AddEmit(first)
	p.AddEmit(second)
	p.Run(context.Background(), src, dst)

	got := dst.snapshot()
	want := []string{"A", "_one:A", "_two:_one:A", "_two:A"}
	if len(got) != len(want) {
		t.Fatalf("broadcasts: got %d events (%v), want %d (%v)",
			len(got), names(got), len(want), want)
	}
	for i, n := range want {
		if got[i].Name != n {
			t.Fatalf("broadcast[%d]: got %q, want %q (full: %v)",
				i, got[i].Name, n, names(got))
		}
	}
}

// TestPipeline_EarlierEmittersDoNotSeeLaterEmissions guards the
// strictly-forward feed rule: `first` must NOT see the event `second`
// produced. If it did, the prefix-guard recursion would still
// terminate but `first` would emit a "_one:_two:A".
func TestPipeline_EarlierEmittersDoNotSeeLaterEmissions(t *testing.T) {
	src := &staticSource{events: []Event{{Name: "A"}}}
	dst := &recordingBroadcaster{}
	first := &prefixEmit{prefix: "_one"}
	second := &prefixEmit{prefix: "_two"}

	p := NewPipeline()
	p.AddEmit(first)
	p.AddEmit(second)
	p.Run(context.Background(), src, dst)

	for _, evt := range dst.snapshot() {
		if evt.Name == "_one:_two:A" {
			t.Fatalf("earlier emitter saw later emission: %v", names(dst.snapshot()))
		}
	}
}

func names(evts []Event) []string {
	out := make([]string, len(evts))
	for i, e := range evts {
		out[i] = e.Name
	}
	return out
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
