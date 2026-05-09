// Package discoveries persists newly-seen Statfeed event names so
// plugin authors can review them before adding them to the verified
// registry (internal/types.VerifiedStatfeedNames).
package discoveries

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Entry tracks how often an unknown Statfeed name has been seen and
// when, so plugin authors can confirm new names before adding them to
// the verified registry.
type Entry struct {
	Name        string    `json:"name"`
	FirstSeenAt time.Time `json:"firstSeenAt"`
	LastSeenAt  time.Time `json:"lastSeenAt"`
	Count       int       `json:"count"`
}

// Store persists newly-seen Statfeed event names to
// data/statfeed-discoveries.json. Writes are debounced — a flurry of
// the same name in one match doesn't hammer the disk.
type Store struct {
	path string

	mu      sync.Mutex
	entries map[string]*Entry

	dirty bool
}

// New loads any existing discoveries from disk (errors are ignored —
// a missing or corrupt file becomes a fresh store) and returns a
// usable store.
func New(dataDir string) *Store {
	path := filepath.Join(dataDir, "statfeed-discoveries.json")
	s := &Store{
		path:    path,
		entries: make(map[string]*Entry),
	}
	bytes, err := os.ReadFile(path)
	if err == nil {
		var list []*Entry
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
func (s *Store) Record(name string) bool {
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
	s.entries[name] = &Entry{
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
func (s *Store) All() []*Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Entry, 0, len(s.entries))
	for _, d := range s.entries {
		copy := *d
		out = append(out, &copy)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].FirstSeenAt.Before(out[j].FirstSeenAt)
	})
	return out
}

// Flush writes the entries to disk if dirty. No-op when nothing
// changed.
func (s *Store) Flush() error {
	s.mu.Lock()
	if !s.dirty {
		s.mu.Unlock()
		return nil
	}
	out := make([]*Entry, 0, len(s.entries))
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
