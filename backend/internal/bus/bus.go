package bus

import (
	"encoding/json"
	"log"
	"sync"
	"time"
)

// subscriberBufSize is the per-subscriber channel depth. Sized to
// absorb GC pauses and browser repaint hitches without dropping a
// healthy consumer, while still evicting a genuinely stalled one
// quickly. The on-the-wire event rate depends on RL's PacketSendRate
// (1..120, recommended 10), so this is a fixed slot count rather
// than a time window — at 10 Hz it's ~6 s of headroom, at 120 Hz
// roughly 0.5 s.
const subscriberBufSize = 64

// Bus fans out raw RL messages to all SSE subscribers.
//
// Critical guarantee: Broadcast never blocks. If a subscriber can't keep up
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
	// _MatchState are framing signals every subscriber needs.
	events map[string]struct{}
}

type Bus struct {
	mu      sync.RWMutex
	subs    map[*subscriber]struct{}
	metrics *busMetrics

	// publishMu serializes calls to Broadcast. Cheap (held only for the
	// scratch snapshot, not for per-subscriber sends) and lets HTTP
	// handlers publish synthetic events alongside the RL dispatcher
	// goroutine — used by /api/overlay/overrides to push reflow events.
	publishMu sync.Mutex

	// scratch is reused by Broadcast to snapshot subscribers without
	// allocating per call. Guarded by publishMu — concurrent publishers
	// would otherwise race on the slice append.
	scratch []*subscriber
}

func NewBus() *Bus {
	return &Bus{
		subs:    make(map[*subscriber]struct{}),
		metrics: newBusMetrics(),
	}
}

// Subscribers returns the current subscriber count. Cheap; uses the read lock.
func (b *Bus) Subscribers() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subs)
}

// Metrics exposes the bus's instrumentation snapshot. Read-only; safe
// for concurrent use.
func (b *Bus) Metrics() MetricsSnapshot {
	return b.metrics.snapshot(b.Subscribers())
}

// Subscribe returns a receive-only channel and a cancel func. The cancel
// func is idempotent. The channel is closed when canceled or when the bus
// drops the subscriber, so receivers must use the `msg, ok := <-ch` form.
//
// events optionally filters which RL events the subscriber wants to
// receive. nil means "all events". Synthetic events with a "_" prefix
// (e.g. _ConnectionStatus, _MatchState) are always delivered — they're
// framing signals the SDK relies on regardless of plugin opt-in.
func (b *Bus) Subscribe(events map[string]struct{}) (<-chan []byte, func()) {
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
// and Broadcast's slow-eviction path without double-closing channels.
func (b *Bus) removeLocked(s *subscriber) {
	if _, ok := b.subs[s]; !ok {
		return
	}
	delete(b.subs, s)
	close(s.closed)
	close(s.ch)
}

// framingSignals lists synthetic event names that bypass the per-subscriber
// filter. Every subscriber needs these regardless of the ?events= filter
// — they're status/lifecycle signals the SDK uses for its own bookkeeping
// (connection state, gameplay phase, roster identity, dev hot-reload).
// Other synthetic events (_StatfeedEvent, _GoalScored, etc.) are
// filterable like normal events: a plugin only receives them if it
// subscribed by name.
var framingSignals = map[string]struct{}{
	"_ConnectionStatus": {},
	"_MatchState":       {},
	"_RosterChanged":    {},
	"_IdentityChanged":  {},
	"_DevPluginReload":  {},
}

func isFramingSignal(eventName string) bool {
	_, ok := framingSignals[eventName]
	return ok
}

// Broadcast delivers an Event to every subscriber. Slow subscribers
// (whose buffer is full) are evicted from the bus — keeping a stuck SSE
// client around would block the read loop and eventually freeze the
// upstream connection.
//
// Subscribers with an event filter only receive matching events. The
// event name is extracted once per Broadcast so the hot path stays
// JSON-decode-free regardless of the user's PacketSendRate (1..120).
//
// Wire shape: when evt.Raw is non-nil (the event arrived over the RL
// socket or a processor passed through bytes it already had), we ship
// Raw verbatim. Otherwise we synthesize an envelope from evt.Name +
// evt.Data so synthetic emissions from processors don't have to
// pre-marshal their own framing.
func (b *Bus) Broadcast(evt Event) {
	if evt.Raw != nil {
		b.broadcastRaw(evt.Name, evt.Raw)
		return
	}
	envelope, err := json.Marshal(struct {
		Event string          `json:"Event"`
		Data  json.RawMessage `json:"Data"`
	}{Event: evt.Name, Data: evt.Data})
	if err != nil {
		return
	}
	b.broadcastRaw(evt.Name, envelope)
}

func (b *Bus) broadcastRaw(eventName string, data []byte) {
	start := time.Now()

	// Framing-signal synthetics bypass the per-subscriber filter — every
	// subscriber needs them regardless of what they listed in ?events=.
	// Other synthetic _-prefixed events (_StatfeedEvent, _GoalScored,
	// _PlayerDemolished, etc.) are filterable like normal events: a
	// plugin only receives them if it subscribed by name. Keeps wire
	// traffic proportional to actual interest now that high-rate events
	// are publishing on every tick of activity.
	bypassFilter := isFramingSignal(eventName)

	// Snapshot under read lock + publishMu so we don't race the scratch
	// slice with another publisher. We copy into a fresh slice owned by
	// this call so the subsequent send loop doesn't share storage with
	// the next publish.
	b.publishMu.Lock()
	b.mu.RLock()
	if cap(b.scratch) < len(b.subs) {
		b.scratch = make([]*subscriber, 0, len(b.subs))
	}
	tmp := b.scratch[:0]
	for s := range b.subs {
		tmp = append(tmp, s)
	}
	b.scratch = tmp
	dst := append([]*subscriber(nil), tmp...)
	b.mu.RUnlock()
	b.publishMu.Unlock()

	var slow []*subscriber
	delivered, filterRejects := 0, 0
	for _, s := range dst {
		// Filter check — framing synthetics bypass to keep
		// _ConnectionStatus / _MatchState / _RosterChanged /
		// _IdentityChanged reliable as universal signals.
		if !bypassFilter && s.events != nil {
			if _, ok := s.events[eventName]; !ok {
				filterRejects++
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
		filterRejects,
		len(slow),
		delivered*len(data),
	)
}
