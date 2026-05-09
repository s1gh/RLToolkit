// Package surface owns data/overlay-surface.json — the user-configured
// overlay surface dimensions used by the editor's canvas. Mirrors
// overrides.Store: load-on-start, in-memory cache, atomic temp+rename,
// optional Notify hook.
//
// Two values: Configured (user setting from dashboard, persisted) and
// Detected (bound monitor size reported by rl-widget, in-memory only).
// Effective() returns Configured ?? Detected ?? {1920, 1080} so the
// editor always has a target to render against.
package surface

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
)

// Size is a width/height pair in logical pixels. Both values are
// positive and bounded by maxSurfacePixel.
type Size struct {
	W int `json:"width"`
	H int `json:"height"`
}

// maxSurfacePixel caps width/height past any realistic monitor (8K is
// 7680px) but well under GPU max-texture limits, so a bad value can't
// allocate a multi-gigabyte canvas.
const maxSurfacePixel = 16384

// Validate returns nil if both axes are in (0, maxSurfacePixel].
func (s Size) Validate() error {
	if s.W <= 0 || s.H <= 0 {
		return fmt.Errorf("surface size must be positive, got %dx%d", s.W, s.H)
	}
	if s.W > maxSurfacePixel || s.H > maxSurfacePixel {
		return fmt.Errorf("surface size must be <= %dx%d, got %dx%d", maxSurfacePixel, maxSurfacePixel, s.W, s.H)
	}
	return nil
}

// Store persists configured surface to disk and tracks the
// rl-widget-reported detected size in memory.
type Store struct {
	path string

	mu         sync.RWMutex
	configured *Size
	detected   *Size

	// Notify, if set, fires after every successful Set. SetDetected
	// does NOT fire Notify — see SetDetected.
	Notify func()
}

// New loads the existing file or starts with a nil configured value.
// A corrupt file is quarantined to <path>.broken and the store starts
// fresh, matching overrides.New.
func New(dir string) (*Store, error) {
	path := filepath.Join(dir, "overlay-surface.json")
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
	var sz Size
	if err := json.Unmarshal(raw, &sz); err != nil {
		broken := path + ".broken"
		if rnErr := os.Rename(path, broken); rnErr != nil {
			log.Printf("[surface] %s is corrupt (%v); also failed to quarantine to %s: %v — starting empty", path, err, broken, rnErr)
		} else {
			log.Printf("[surface] %s is corrupt (%v); moved to %s — starting empty", path, err, broken)
		}
		return s, nil
	}
	if err := sz.Validate(); err != nil {
		broken := path + ".broken"
		if rnErr := os.Rename(path, broken); rnErr != nil {
			log.Printf("[surface] %s holds invalid value (%v); also failed to quarantine to %s: %v — starting empty", path, err, broken, rnErr)
		} else {
			log.Printf("[surface] %s holds invalid value (%v); moved to %s — starting empty", path, err, broken)
		}
		return s, nil
	}
	s.configured = &sz
	return s, nil
}

// Get returns a copy of the configured size, or nil if unset.
func (s *Store) Get() *Size {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.configured == nil {
		return nil
	}
	c := *s.configured
	return &c
}

// Detected returns a copy of the detected size, or nil if rl-widget
// hasn't reported one this session.
func (s *Store) Detected() *Size {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.detected == nil {
		return nil
	}
	d := *s.detected
	return &d
}

// Effective returns configured ?? detected ?? {1920, 1080}. The editor
// uses this directly so it never has to encode the priority logic.
func (s *Store) Effective() Size {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.configured != nil {
		return *s.configured
	}
	if s.detected != nil {
		return *s.detected
	}
	return Size{W: 1920, H: 1080}
}

// Set replaces (or with size=nil, clears) the configured value, persists
// to disk, and fires Notify. Validation runs before any state change.
func (s *Store) Set(size *Size) error {
	if size != nil {
		if err := size.Validate(); err != nil {
			return err
		}
	}
	s.mu.Lock()
	prev := s.configured
	s.configured = size
	if err := s.persistLocked(); err != nil {
		s.configured = prev
		s.mu.Unlock()
		return err
	}
	notify := s.Notify
	s.mu.Unlock()
	s.fireNotify(notify)
	return nil
}

// SetDetected stores an in-memory detected size. Doesn't persist and
// doesn't fire Notify: detected only affects Effective() when
// configured is nil, and the launcher always boots before the editor
// opens so the editor's first GET sees the detected value.
func (s *Store) SetDetected(size *Size) error {
	if size != nil {
		if err := size.Validate(); err != nil {
			return err
		}
	}
	s.mu.Lock()
	if size == nil {
		s.detected = nil
	} else {
		c := *size
		s.detected = &c
	}
	s.mu.Unlock()
	return nil
}

// fireNotify is panic-protected and called outside the lock so a
// buggy subscriber can't deadlock the writer.
func (s *Store) fireNotify(notify func()) {
	if notify == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[surface] notify panic: %v", r)
		}
	}()
	notify()
}

// persistLocked writes (or removes) the file. Caller holds s.mu.
func (s *Store) persistLocked() error {
	if s.configured == nil {
		// Clearing: remove the file so future New starts from "no
		// configured value" rather than "{}".
		if err := os.Remove(s.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove %s: %w", s.path, err)
		}
		return nil
	}
	raw, err := json.MarshalIndent(s.configured, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal surface: %w", err)
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
