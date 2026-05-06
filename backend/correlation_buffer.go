package main

import "sync"

// correlationEntry is a single item in the ring buffer.
type correlationEntry struct {
	name    string
	payload interface{}
}

// CorrelationBuffer keeps a sliding window of recent events keyed by
// name so that synthetic event emitters can look back for related
// events (e.g., a GoalScored looking back for a StatfeedEvent that
// carries a modifier flag).
//
// The buffer is event-aligned, not time-aligned: capacity counts
// events written, not seconds elapsed or RL ticks. PacketSendRate
// changes the wall-clock spacing between writes but not how many
// events of context surround any given target event, so lookback
// sizes here are independent of RL's configured tick rate.
type CorrelationBuffer struct {
	mu       sync.Mutex
	capacity int
	entries  []correlationEntry
}

// NewCorrelationBuffer creates a buffer with the given capacity.
func NewCorrelationBuffer(capacity int) *CorrelationBuffer {
	return &CorrelationBuffer{
		capacity: capacity,
		entries:  make([]correlationEntry, 0, capacity),
	}
}

// Record stores an event in the buffer. If the buffer is full, the oldest
// entry is evicted.
func (c *CorrelationBuffer) Record(name string, payload interface{}) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if len(c.entries) >= c.capacity {
		c.entries = c.entries[1:]
	}
	c.entries = append(c.entries, correlationEntry{name: name, payload: payload})
}

// FindWithin searches backward through the buffer for the most recent
// event with the given name that satisfies the predicate. `lookback`
// is the maximum number of events to scan. Returns nil if no match.
func (c *CorrelationBuffer) FindWithin(name string, lookback int, predicate func(interface{}) bool) interface{} {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Search from newest to oldest.
	for i := len(c.entries) - 1; i >= 0 && lookback > 0; i-- {
		e := c.entries[i]
		if e.name == name && predicate(e.payload) {
			return e.payload
		}
		lookback--
	}
	return nil
}

// Recent returns the last N events with the given name, newest first.
func (c *CorrelationBuffer) Recent(name string, n int) []interface{} {
	c.mu.Lock()
	defer c.mu.Unlock()

	var out []interface{}
	for i := len(c.entries) - 1; i >= 0 && len(out) < n; i-- {
		if c.entries[i].name == name {
			out = append(out, c.entries[i].payload)
		}
	}
	return out
}

// RemoveByName removes all entries with the given name where the
// predicate returns true. Used to consume correlation entries so they
// are not matched again by a later event (e.g., goal modifier
// statfeeds that should only apply to one goal).
func (c *CorrelationBuffer) RemoveByName(name string, predicate func(interface{}) bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	n := 0
	for _, e := range c.entries {
		if e.name == name && predicate(e.payload) {
			continue
		}
		c.entries[n] = e
		n++
	}
	c.entries = c.entries[:n]
}
