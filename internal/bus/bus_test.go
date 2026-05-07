package bus

import (
	"encoding/json"
	"testing"
	"time"
)

// TestBus_FanOut covers the basic happy path: two subscribers, both
// receive a broadcast event. Integration tests against the rest of
// the pipeline live in package backend.
func TestBus_FanOut(t *testing.T) {
	b := NewBus()
	a, cancelA := b.Subscribe(nil)
	defer cancelA()
	c, cancelC := b.Subscribe(nil)
	defer cancelC()

	body, _ := json.Marshal(map[string]any{"hello": "world"})
	b.Broadcast(Event{Name: "_Test", Data: body})

	for i, ch := range []<-chan []byte{a, c} {
		select {
		case msg := <-ch:
			if len(msg) == 0 {
				t.Fatalf("subscriber %d: empty frame", i)
			}
		case <-time.After(100 * time.Millisecond):
			t.Fatalf("subscriber %d: no message in 100ms", i)
		}
	}
}

// TestBus_EventFilter covers the per-subscriber filter: a subscriber
// that opted into a different event name should not receive _Test.
func TestBus_EventFilter(t *testing.T) {
	b := NewBus()
	want, cancelWant := b.Subscribe(map[string]struct{}{"_Test": {}})
	defer cancelWant()
	skip, cancelSkip := b.Subscribe(map[string]struct{}{"_Other": {}})
	defer cancelSkip()

	b.Broadcast(Event{Name: "_Test", Data: []byte("{}")})

	select {
	case <-want:
		// expected
	case <-time.After(100 * time.Millisecond):
		t.Fatal("opted-in subscriber missed _Test")
	}

	select {
	case <-skip:
		t.Fatal("opted-out subscriber received _Test it didn't ask for")
	case <-time.After(50 * time.Millisecond):
		// expected
	}
}

// TestBus_FramingSignalBypass covers the framing-signal allowlist:
// a subscriber that opted into one specific event still gets the
// framing synthetics regardless of filter.
func TestBus_FramingSignalBypass(t *testing.T) {
	b := NewBus()
	ch, cancel := b.Subscribe(map[string]struct{}{"_Other": {}})
	defer cancel()

	b.Broadcast(Event{Name: "_MatchState", Data: []byte("{}")})

	select {
	case <-ch:
		// expected
	case <-time.After(100 * time.Millisecond):
		t.Fatal("framing signal _MatchState was filtered out")
	}
}
