package main

import (
	"bytes"
	"log"
	"sync"
	"time"
)

// EventBus fans out raw RL messages to all SSE subscribers.
//
// Critical guarantee: Publish never blocks. If a subscriber can't keep up
// they're immediately evicted (channel closed, removed from bus). Blocking
// here would back up the upstream readLoop, fill RL's TCP send buffer, and
// — on Linux/Proton — freeze RL's main game thread inside send(). Drops
// over hangs, every time.
type subscriber struct {
	ch     chan []byte   // delivered to the SSE handler
	closed chan struct{} // signals the subscriber has disconnected

	// events is the optional event-name filter. nil means "deliver
	// everything"; an empty-but-non-nil map means "deliver nothing"
	// (no plugin would set that, but the distinction matters for
	// correctness). Synthetic events (those starting with "_") are
	// always delivered regardless of filter — _ConnectionStatus and
	// _Lifecycle are framing signals every subscriber needs.
	events map[string]struct{}
}

type EventBus struct {
	mu      sync.RWMutex
	subs    map[*subscriber]struct{}
	metrics *busMetrics

	// scratch is reused by Publish to snapshot subscribers without
	// allocating per call. Safe because only the single dispatcher
	// goroutine calls Publish (serialized through RLClient.dispatcher).
	scratch []*subscriber
}

func NewEventBus() *EventBus {
	return &EventBus{
		subs:    make(map[*subscriber]struct{}),
		metrics: newBusMetrics(),
	}
}

// Subscribers returns the current subscriber count. Cheap; uses the read lock.
func (b *EventBus) Subscribers() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subs)
}

// Metrics exposes the bus's instrumentation snapshot. Read-only; safe
// for concurrent use.
func (b *EventBus) Metrics() metricsSnapshot {
	return b.metrics.snapshot(b.Subscribers())
}

// Subscribe returns a receive-only channel and a cancel func. The cancel
// func is idempotent. The channel is closed when canceled or when the bus
// drops the subscriber, so receivers must use the `msg, ok := <-ch` form.
//
// events optionally filters which RL events the subscriber wants to
// receive. nil means "all events". Synthetic events with a "_" prefix
// (e.g. _ConnectionStatus, _Lifecycle) are always delivered — they're
// framing signals the SDK relies on regardless of plugin opt-in.
func (b *EventBus) Subscribe(events map[string]struct{}) (<-chan []byte, func()) {
	s := &subscriber{
		ch:     make(chan []byte, subscriberBufSize),
		closed: make(chan struct{}),
		events: events,
	}
	b.mu.Lock()
	b.subs[s] = struct{}{}
	b.mu.Unlock()

	var once sync.Once
	return s.ch, func() {
		once.Do(func() {
			b.mu.Lock()
			b.removeLocked(s)
			b.mu.Unlock()
		})
	}
}

// removeLocked detaches a subscriber from the bus. Caller must hold b.mu.
// The existence check makes it safe to call from both Subscribe's cancel
// and Publish's slow-eviction path without double-closing channels.
func (b *EventBus) removeLocked(s *subscriber) {
	if _, ok := b.subs[s]; !ok {
		return
	}
	delete(b.subs, s)
	close(s.closed)
	close(s.ch)
}

// Publish delivers data to every subscriber. Slow subscribers (whose buffer
// is full) are evicted from the bus — keeping a stuck SSE client around
// would block the read loop and eventually freeze the upstream connection.
//
// Subscribers with an event filter only receive matching events. The
// event name is extracted once per Publish via substring scan so the
// 60-120Hz hot path stays JSON-decode-free.
func (b *EventBus) Publish(data []byte) {
	start := time.Now()

	// Extract the event name once, used per subscriber below.
	eventName := extractEventName(data)
	synthetic := len(eventName) > 0 && eventName[0] == '_'

	// Snapshot under read lock so we don't hold it during sends.
	b.mu.RLock()
	if cap(b.scratch) < len(b.subs) {
		b.scratch = make([]*subscriber, 0, len(b.subs))
	}
	dst := b.scratch[:0]
	for s := range b.subs {
		dst = append(dst, s)
	}
	b.scratch = dst
	b.mu.RUnlock()

	var slow []*subscriber
	delivered, skipped := 0, 0
	for _, s := range dst {
		// Filter check — synthetic events bypass to keep _ConnectionStatus
		// and _Lifecycle reliable as framing signals.
		if !synthetic && s.events != nil {
			if _, ok := s.events[eventName]; !ok {
				skipped++
				continue
			}
		}
		select {
		case <-s.closed:
		case s.ch <- data:
			delivered++
		default:
			slow = append(slow, s)
		}
	}
	if len(slow) > 0 {
		b.mu.Lock()
		for _, s := range slow {
			b.removeLocked(s)
		}
		b.mu.Unlock()
		log.Printf("[bus] dropped %d slow subscriber(s)", len(slow))
	}

	b.metrics.recordPublish(
		time.Since(start).Nanoseconds(),
		delivered,
		skipped,
		len(slow),
		delivered*len(data),
	)
}

// extractEventName pulls the "Event":"X" value from an RL-shaped JSON
// envelope without a full Unmarshal. Returns "" if the field is absent
// or malformed; callers treat empty as "no filter applies, deliver".
func extractEventName(raw []byte) string {
	// Bound the scan to the head of the envelope — Event is reliably
	// near the start, and a malformed/giant payload shouldn't make us
	// walk the whole thing.
	head := raw
	if len(head) > 96 {
		head = head[:96]
	}
	i := bytes.Index(head, eventKeyMarker)
	if i < 0 {
		return ""
	}
	rest := raw[i+len(eventKeyMarker):]
	end := bytes.IndexByte(rest, '"')
	if end < 0 {
		return ""
	}
	return string(rest[:end])
}

var eventKeyMarker = []byte(`"Event":"`)
