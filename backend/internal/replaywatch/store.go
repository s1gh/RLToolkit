package replaywatch

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// storeFileName is the on-disk file under <dataDir>.
const storeFileName = "replay-watcher.json"

// Store persists the user's replay-directory override. Mirrors
// surface.Store: load on New, atomic temp+rename, optional Notify.
type Store struct {
	path string

	mu  sync.RWMutex
	dir string

	// Notify, if set, fires after every successful Set (regardless of
	// whether the value actually changed) — matches surface.Store.
	Notify func()
}

// fileShape is the on-disk JSON. Empty file or missing key both mean
// "no override".
type fileShape struct {
	Dir string `json:"dir"`
}

// NewStore loads the existing file or starts with no override. A
// corrupt file is quarantined to <path>.broken and the store starts
// fresh, matching surface.New / overrides.New.
func NewStore(dir string) (*Store, error) {
	path := filepath.Join(dir, storeFileName)
	s := &Store{path: path}
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return s, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if len(raw) == 0 {
		return s, nil
	}
	var fs fileShape
	if err := json.Unmarshal(raw, &fs); err != nil {
		broken := path + ".broken"
		if rnErr := os.Rename(path, broken); rnErr != nil {
			log.Printf("[replaywatch] %s is corrupt (%v); also failed to quarantine to %s: %v — starting empty", path, err, broken, rnErr)
		} else {
			log.Printf("[replaywatch] %s is corrupt (%v); moved to %s — starting empty", path, err, broken)
		}
		return s, nil
	}
	s.dir = fs.Dir
	return s, nil
}

// Get returns the configured override, or "" when unset.
func (s *Store) Get() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.dir
}

// Set replaces the override (empty string clears it), persists to
// disk, and fires Notify. Validation runs before any state change.
func (s *Store) Set(dir string) error {
	if err := validateDir(dir); err != nil {
		return err
	}
	s.mu.Lock()
	prev := s.dir
	s.dir = dir
	if err := s.persistLocked(); err != nil {
		s.dir = prev
		s.mu.Unlock()
		return err
	}
	notify := s.Notify
	s.mu.Unlock()
	s.fireNotify(notify)
	return nil
}

// validateDir rejects strings with embedded NUL bytes. Existence is not
// checked here — runtime status reports the truth.
func validateDir(dir string) error {
	if strings.ContainsRune(dir, 0) {
		return errors.New("dir contains NUL byte")
	}
	return nil
}

func (s *Store) fireNotify(notify func()) {
	if notify == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[replaywatch] notify panic: %v", r)
		}
	}()
	notify()
}

func (s *Store) persistLocked() error {
	if s.dir == "" {
		// Clearing: remove the file so future NewStore starts from "no
		// configured value" rather than "{}".
		if err := os.Remove(s.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove %s: %w", s.path, err)
		}
		return nil
	}
	raw, err := json.MarshalIndent(fileShape{Dir: s.dir}, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("rename %s -> %s: %w", tmp, s.path, err)
	}
	return nil
}
