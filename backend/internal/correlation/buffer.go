// Package correlation implements a sliding-window event buffer used by
// synthetic-event emitters to look back for related events (e.g. a
// GoalScored looking back for a StatfeedEvent that carries a modifier
// flag).
//
// The buffer is event-aligned, not time-aligned: capacity counts
// events written, not seconds elapsed or RL ticks. PacketSendRate
// changes the wall-clock spacing between writes but not how many
// events of context surround any given target event, so lookback
// sizes here are independent of RL's configured tick rate.
package correlation

import "sync"

type entry struct {
	name    string
	payload interface{}
}

// Buffer keeps a sliding window of recent events keyed by name.
type Buffer struct {
	mu       sync.Mutex
	capacity int
	entries  []entry
}

// New creates a buffer with the given capacity.
func New(capacity int) *Buffer {
	return &Buffer{
		capacity: capacity,
		entries:  make([]entry, 0, capacity),
	}
}

// Record stores an event in the buffer. If the buffer is full, the oldest
// entry is evicted.
func (c *Buffer) Record(name string, payload interface{}) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if len(c.entries) >= c.capacity {
		c.entries = c.entries[1:]
	}
	c.entries = append(c.entries, entry{name: name, payload: payload})
}

// FindWithin searches backward through the buffer for the most recent
// event with the given name that satisfies the predicate. `lookback`
// is the maximum number of events to scan. Returns nil if no match.
func (c *Buffer) FindWithin(name string, lookback int, predicate func(interface{}) bool) interface{} {
	c.mu.Lock()
	defer c.mu.Unlock()

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
func (c *Buffer) Recent(name string, n int) []interface{} {
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
func (c *Buffer) RemoveByName(name string, predicate func(interface{}) bool) {
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

// MixedEntry is one entry from WalkRecent, carrying the event name
// alongside the payload so callers can branch on type while keeping
// the chronological ordering between unrelated event kinds.
type MixedEntry struct {
	Name    string
	Payload interface{}
}

// WalkRecent returns up to `limit` most recent entries whose name
// appears in `names`, walking the buffer newest-first. The returned
// slice preserves chronological order across event kinds — used when
// a caller needs to check whether one event happened after another
// (e.g., "did this teammate's Shot statfeed come after any opponent
// BallHit in the buffer?"), which the per-name Recent() can't answer.
func (c *Buffer) WalkRecent(names []string, limit int) []MixedEntry {
	c.mu.Lock()
	defer c.mu.Unlock()

	wanted := make(map[string]struct{}, len(names))
	for _, n := range names {
		wanted[n] = struct{}{}
	}
	out := make([]MixedEntry, 0, limit)
	for i := len(c.entries) - 1; i >= 0 && len(out) < limit; i-- {
		e := c.entries[i]
		if _, ok := wanted[e.name]; !ok {
			continue
		}
		out = append(out, MixedEntry{Name: e.name, Payload: e.payload})
	}
	return out
}
