package main

import "sync"

// correlationEntry is a single item in the ring buffer.
type correlationEntry struct {
	name    string
	payload interface{}
}

// CorrelationBuffer keeps a sliding window of recent events keyed by
// name so that synthetic event emitters can look back a small number of
// ticks for related events (e.g., a GoalScored looking back for a
// StatfeedEvent that carries a modifier flag).
//
// The buffer is NOT tick-aligned; it is event-aligned. "Ticks" are
// approximated by the number of events recorded since the target event.
// At 60 Hz with ~5 events per tick, a capacity of 15 covers ~3 ticks.
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
// event with the given name that satisfies the predicate. `ticks` is
// the maximum number of events to look back. Returns nil if no match.
func (c *CorrelationBuffer) FindWithin(name string, ticks int, predicate func(interface{}) bool) interface{} {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Search from newest to oldest.
	for i := len(c.entries) - 1; i >= 0 && ticks > 0; i-- {
		e := c.entries[i]
		if e.name == name && predicate(e.payload) {
			return e.payload
		}
		ticks--
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
