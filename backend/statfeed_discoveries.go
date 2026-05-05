package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// verifiedStatfeedNames is the registry of StatfeedEvent.EventName
// values the toolkit knows about. Anything NOT in this set is treated
// as "discovered" — the synthesizer publishes _UnknownStatfeed and
// (optionally) the discovery store records first/last-seen + count.
//
// This is broader than the SDK's `RLT.stats` object (which exposes
// only the 10 most-used names as ergonomic constants for plugin
// authors). The two don't have to match: the SDK set is plugin-facing
// API surface, this set is the "we already know about this name"
// filter for the discovery channel.
var verifiedStatfeedNames = map[string]struct{}{
	// Promoted variants — each has its own _-prefixed synthetic event
	// alongside the catch-all _StatfeedEvent. The list below mirrors
	// the case branches in emitStatfeedVariant (synthesizer.go).
	"Shot":       {},
	"HatTrick":   {},
	"Save":       {},
	"EpicSave":   {},
	"Demolish":   {},
	"FlipReset":  {},
	"Assist":     {},
	"Center":     {},
	"Clear":      {},
	"BicycleHit": {},
	// Goal modifiers — collected onto _GoalScored.modifiers (see
	// modifierStatfeedNames in synthesizer.go) but the catch-all
	// _StatfeedEvent also ships them, so they belong here. The list
	// mirrors modifierStatfeedNames exactly.
	"AerialGoal":     {},
	"BackwardsGoal":  {},
	"BicycleGoal":    {},
	"LongGoal":       {},
	"TurtleGoal":     {},
	"OvertimeGoal":   {},
	"PoolShot":       {},
	"HoopsSwishGoal": {},
	// Stat events without a dedicated synthetic envelope. Catch-all
	// _StatfeedEvent still fires; we list them here so RL emitting
	// them doesn't trip _UnknownStatfeed. The first seven match the
	// "intentionally untracked" set called out in emitStatfeedVariant's
	// comment; the rest are observed in-game and have no synthetic
	// equivalent yet.
	"Goal":       {},
	"Win":        {},
	"MVP":        {},
	"Playmaker":  {},
	"Savior":     {},
	"LowFive":    {},
	"HighFive":   {},
	"FirstTouch": {},
	"Demolition": {},
	"OwnGoal":    {},
}

// StatfeedDiscovery is one entry in the persisted discoveries map. It
// tracks how often an unknown name has been seen and when. Plugin
// authors use this to confirm new names before adding them to the
// verified registry.
type StatfeedDiscovery struct {
	Name        string    `json:"name"`
	FirstSeenAt time.Time `json:"firstSeenAt"`
	LastSeenAt  time.Time `json:"lastSeenAt"`
	Count       int       `json:"count"`
}

// StatfeedDiscoveryStore persists newly-seen Statfeed event names to
// data/statfeed-discoveries.json. Writes are debounced — a flurry of
// the same name in one match doesn't hammer the disk.
type StatfeedDiscoveryStore struct {
	path string

	mu      sync.Mutex
	entries map[string]*StatfeedDiscovery

	// dirty is set when entries has unflushed writes; the flush loop
	// reads + clears it. Avoids reflushing on every Record call.
	dirty bool
}

// NewStatfeedDiscoveryStore loads any existing discoveries from disk
// (errors are ignored — a missing or corrupt file becomes a fresh
// store) and returns a usable store.
func NewStatfeedDiscoveryStore(dataDir string) *StatfeedDiscoveryStore {
	path := filepath.Join(dataDir, "statfeed-discoveries.json")
	s := &StatfeedDiscoveryStore{
		path:    path,
		entries: make(map[string]*StatfeedDiscovery),
	}
	bytes, err := os.ReadFile(path)
	if err == nil {
		var list []*StatfeedDiscovery
		if err := json.Unmarshal(bytes, &list); err == nil {
			for _, d := range list {
				s.entries[d.Name] = d
			}
		}
	}
	return s
}

// Record bumps the count + lastSeenAt for an unknown name. Returns
// true if this is the first observation of the name (so the caller
// can highlight it in the UI / logs).
func (s *StatfeedDiscoveryStore) Record(name string) bool {
	if name == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if d, ok := s.entries[name]; ok {
		d.LastSeenAt = now
		d.Count++
		s.dirty = true
		return false
	}
	s.entries[name] = &StatfeedDiscovery{
		Name:        name,
		FirstSeenAt: now,
		LastSeenAt:  now,
		Count:       1,
	}
	s.dirty = true
	return true
}

// All returns a sorted (by FirstSeenAt) snapshot of every recorded
// discovery. Used by the /api/statfeed-discoveries endpoint.
func (s *StatfeedDiscoveryStore) All() []*StatfeedDiscovery {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*StatfeedDiscovery, 0, len(s.entries))
	for _, d := range s.entries {
		copy := *d
		out = append(out, &copy)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].FirstSeenAt.Before(out[j].FirstSeenAt)
	})
	return out
}

// Flush writes the entries to disk if dirty. Cheap when nothing
// changed (no write). Errors are returned so callers can decide
// whether to log; the synthesizer's flush loop logs once and continues.
func (s *StatfeedDiscoveryStore) Flush() error {
	s.mu.Lock()
	if !s.dirty {
		s.mu.Unlock()
		return nil
	}
	out := make([]*StatfeedDiscovery, 0, len(s.entries))
	for _, d := range s.entries {
		copy := *d
		out = append(out, &copy)
	}
	s.dirty = false
	s.mu.Unlock()

	sort.Slice(out, func(i, j int) bool {
		return out[i].FirstSeenAt.Before(out[j].FirstSeenAt)
	})
	bytes, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(s.path, bytes, 0o644)
}
