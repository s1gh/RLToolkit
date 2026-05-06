package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
)

// Identity is the persisted "who am I" answer the backend uses to
// stamp EnrichedPlayer.IsMe. The PrimaryID is the canonical
// "Platform|UserId|SubId" form RL ships on UpdateState; Name is kept
// for diagnostics (UI labels, log lines).
type Identity struct {
	PrimaryID string `json:"primaryId"`
	Name      string `json:"name"`
}

// IdentityStore persists a single Identity to identity.json under
// the data directory. Reads are common (every roster resolve), writes
// are rare (user picks themselves on the dashboard) — so the storage
// is just one JSON file rewritten in place.
//
// Notify, when set, is invoked from outside the lock whenever the
// stored identity changes. main.go wires it to broadcast
// _IdentityChanged through the bus so plugins can react without
// polling.
type IdentityStore struct {
	path string

	mu  sync.RWMutex
	cur *Identity

	Notify func(*Identity)
}

// NewIdentityStore opens (or creates) identity.json under dataDir
// and seeds the in-memory cache from disk. A missing file is fine —
// the store starts empty until Set is called.
func NewIdentityStore(dataDir string) (*IdentityStore, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, err
	}
	s := &IdentityStore{path: filepath.Join(dataDir, "identity.json")}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *IdentityStore) load() error {
	body, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if len(body) == 0 {
		return nil
	}
	var id Identity
	if err := json.Unmarshal(body, &id); err != nil {
		return err
	}
	if id.PrimaryID == "" {
		return nil
	}
	s.mu.Lock()
	s.cur = &id
	s.mu.Unlock()
	return nil
}

// Get returns a copy of the current identity, or nil when none is
// set. The copy keeps callers from mutating the stored value.
func (s *IdentityStore) Get() *Identity {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.cur == nil {
		return nil
	}
	cp := *s.cur
	return &cp
}

// Set persists a new identity and fires Notify (if set) outside the
// lock. PrimaryID is required; an empty value is treated as a
// programming error.
func (s *IdentityStore) Set(id Identity) error {
	if id.PrimaryID == "" {
		return errors.New("primaryId required")
	}
	body, err := json.Marshal(id)
	if err != nil {
		return err
	}
	if err := os.WriteFile(s.path, body, 0o644); err != nil {
		return err
	}
	s.mu.Lock()
	s.cur = &id
	notify := s.Notify
	s.mu.Unlock()
	if notify != nil {
		cp := id
		notify(&cp)
	}
	return nil
}

// Clear removes the on-disk identity and zeroes the cache. Notify
// fires with nil.
func (s *IdentityStore) Clear() error {
	if err := os.Remove(s.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	s.mu.Lock()
	s.cur = nil
	notify := s.Notify
	s.mu.Unlock()
	if notify != nil {
		notify(nil)
	}
	return nil
}
