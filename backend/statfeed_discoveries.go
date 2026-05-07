package backend

import (
	"encoding/json"
	"os"
	"path/filepath"
	"rl-toolkit/internal/types"
	"sort"
	"sync"
	"time"
)

// verifiedStatfeedNames now lives in internal/types so emit and
// assets can both reference it without depending on this file.
var verifiedStatfeedNames = types.VerifiedStatfeedNames

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
