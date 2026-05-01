package main

import (
	"log"
	"sync"
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
}

type EventBus struct {
	mu   sync.RWMutex
	subs map[*subscriber]struct{}
}

func NewEventBus() *EventBus {
	return &EventBus{subs: make(map[*subscriber]struct{})}
}

// Subscribe returns a receive-only channel and a cancel func. The cancel
// func is idempotent. The channel is closed when canceled or when the bus
// drops the subscriber, so receivers must use the `msg, ok := <-ch` form.
func (b *EventBus) Subscribe() (<-chan []byte, func()) {
	s := &subscriber{
		ch:     make(chan []byte, subscriberBufSize),
		closed: make(chan struct{}),
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
func (b *EventBus) Publish(data []byte) {
	// Snapshot under read lock so we don't hold it during sends.
	b.mu.RLock()
	dst := make([]*subscriber, 0, len(b.subs))
	for s := range b.subs {
		dst = append(dst, s)
	}
	b.mu.RUnlock()

	var slow []*subscriber
	for _, s := range dst {
		select {
		case <-s.closed:
		case s.ch <- data:
		default:
			slow = append(slow, s)
		}
	}
	if len(slow) == 0 {
		return
	}
	b.mu.Lock()
	for _, s := range slow {
		b.removeLocked(s)
	}
	b.mu.Unlock()
	log.Printf("[bus] dropped %d slow subscriber(s)", len(slow))
}
