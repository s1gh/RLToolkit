// Package overrides owns data/overlay-overrides.json — the per-user
// override layer for plugin manifest overlay blocks. Loaded at
// startup, held in memory, persisted via atomic temp+rename on every
// merge or delete.
package overrides

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"

	"rl-toolkit/backend/internal/plugins"
)

// Override is the per-user override of a plugin manifest's overlay
// block. Every field is optional — a partial override layers cleanly
// over the manifest defaults via shallow merge on the JS side. Pointers
// let us distinguish "field not set in override" (nil; manifest wins)
// from "field set to its zero value" (e.g., explicit OffsetX=0 still
// wins over a non-zero manifest default).
type Override struct {
	Anchor  *string             `json:"anchor,omitempty"`
	OffsetX *int                `json:"offset_x,omitempty"`
	OffsetY *int                `json:"offset_y,omitempty"`
	Width   *plugins.Dimension  `json:"width,omitempty"`
	Height  *plugins.Dimension  `json:"height,omitempty"`
	Opacity *float64            `json:"opacity,omitempty"`
	// Enabled gates whether the plugin's overlay iframe is mounted.
	// Pointer so nil means "no preference; default = true". When
	// explicitly false, /overlay skips the plugin entirely.
	Enabled *bool `json:"enabled,omitempty"`
	// Min / Max content-size clamps. Used when the corresponding axis
	// resolves to "auto" (either from the manifest or from this
	// override). Ignored on fixed-pixel axes. Pointer so nil means
	// "no override; manifest value wins".
	MinWidth  *int `json:"min_width,omitempty"`
	MaxWidth  *int `json:"max_width,omitempty"`
	MinHeight *int `json:"min_height,omitempty"`
	MaxHeight *int `json:"max_height,omitempty"`
}

// validAnchors are the four corner strings the production overlay
// recognizes. Anything else is rejected at the API boundary so the file
// never holds a value the renderer can't honor.
var validAnchors = map[string]struct{}{
	"top-left":     {},
	"top-right":    {},
	"bottom-left":  {},
	"bottom-right": {},
}

// maxOverlayPixel caps overlay dimensions and offsets at a value larger
// than any practical streaming canvas (8K wide is 7680px). Rejects
// hand-edited junk that would otherwise render off-screen and become
// unrecoverable from the editor UI.
const maxOverlayPixel = 8192

// Validate returns an error if any present field carries a disallowed
// value. Absent fields (nil pointers) are always allowed — they mean
// "fall back to the manifest."
func (o *Override) Validate() error {
	if o.Anchor != nil {
		if _, ok := validAnchors[*o.Anchor]; !ok {
			return fmt.Errorf("anchor %q must be top-left, top-right, bottom-left, or bottom-right", *o.Anchor)
		}
	}
	if o.OffsetX != nil && (*o.OffsetX < 0 || *o.OffsetX > maxOverlayPixel) {
		return fmt.Errorf("offset_x must be between 0 and %d, got %d", maxOverlayPixel, *o.OffsetX)
	}
	if o.OffsetY != nil && (*o.OffsetY < 0 || *o.OffsetY > maxOverlayPixel) {
		return fmt.Errorf("offset_y must be between 0 and %d, got %d", maxOverlayPixel, *o.OffsetY)
	}
	if o.Width != nil && !o.Width.Auto && (o.Width.Px < 0 || o.Width.Px > maxOverlayPixel) {
		return fmt.Errorf("width must be between 0 and %d, got %d", maxOverlayPixel, o.Width.Px)
	}
	if o.Height != nil && !o.Height.Auto && (o.Height.Px < 0 || o.Height.Px > maxOverlayPixel) {
		return fmt.Errorf("height must be between 0 and %d, got %d", maxOverlayPixel, o.Height.Px)
	}
	if o.Opacity != nil && (*o.Opacity < 0 || *o.Opacity > 1) {
		return fmt.Errorf("opacity must be between 0 and 1, got %g", *o.Opacity)
	}
	if o.MinWidth != nil && (*o.MinWidth < 0 || *o.MinWidth > maxOverlayPixel) {
		return fmt.Errorf("min_width must be between 0 and %d, got %d", maxOverlayPixel, *o.MinWidth)
	}
	if o.MaxWidth != nil && (*o.MaxWidth < 0 || *o.MaxWidth > maxOverlayPixel) {
		return fmt.Errorf("max_width must be between 0 and %d, got %d", maxOverlayPixel, *o.MaxWidth)
	}
	if o.MinHeight != nil && (*o.MinHeight < 0 || *o.MinHeight > maxOverlayPixel) {
		return fmt.Errorf("min_height must be between 0 and %d, got %d", maxOverlayPixel, *o.MinHeight)
	}
	if o.MaxHeight != nil && (*o.MaxHeight < 0 || *o.MaxHeight > maxOverlayPixel) {
		return fmt.Errorf("max_height must be between 0 and %d, got %d", maxOverlayPixel, *o.MaxHeight)
	}
	return nil
}

// merge layers `partial` on top of `o`, leaving `o`'s fields intact when
// the partial omits them. Returns a new value rather than mutating in
// place so callers can't accidentally hold a half-updated reference.
func (o Override) merge(partial Override) Override {
	if partial.Anchor != nil {
		o.Anchor = partial.Anchor
	}
	if partial.OffsetX != nil {
		o.OffsetX = partial.OffsetX
	}
	if partial.OffsetY != nil {
		o.OffsetY = partial.OffsetY
	}
	if partial.Width != nil {
		o.Width = partial.Width
	}
	if partial.Height != nil {
		o.Height = partial.Height
	}
	if partial.Opacity != nil {
		o.Opacity = partial.Opacity
	}
	if partial.Enabled != nil {
		o.Enabled = partial.Enabled
	}
	if partial.MinWidth != nil {
		o.MinWidth = partial.MinWidth
	}
	if partial.MaxWidth != nil {
		o.MaxWidth = partial.MaxWidth
	}
	if partial.MinHeight != nil {
		o.MinHeight = partial.MinHeight
	}
	if partial.MaxHeight != nil {
		o.MaxHeight = partial.MaxHeight
	}
	return o
}

// Store owns data/overlay-overrides.json. Loaded once at startup and
// held in memory; writes flush the whole map back to disk under a
// single mutex. The file is small (one entry per plugin) so full
// rewrites per change are fine.
type Store struct {
	path string

	mu   sync.RWMutex
	data map[string]Override

	// Notify, if set, fires after every successful persist (MergeOne /
	// Delete) outside the lock. Wired by main to publish a synthetic
	// _OverridesChanged SSE event so /overlay clients can reflow in place.
	Notify func()
}

// New loads the existing file (or starts with an empty map when the
// file doesn't exist). On parse failure the corrupt file is
// quarantined to <path>.broken and the store starts empty — the
// server always starts, the user's broken file is preserved for
// investigation, and the editor's first save creates a fresh valid
// file.
func New(dir string) (*Store, error) {
	path := filepath.Join(dir, "overlay-overrides.json")
	s := &Store{
		path: path,
		data: map[string]Override{},
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if len(raw) == 0 {
		return s, nil
	}
	if err := json.Unmarshal(raw, &s.data); err != nil {
		broken := path + ".broken"
		if rnErr := os.Rename(path, broken); rnErr != nil {
			log.Printf("[overrides] %s is corrupt (%v); also failed to quarantine to %s: %v — starting empty", path, err, broken, rnErr)
		} else {
			log.Printf("[overrides] %s is corrupt (%v); moved to %s — starting empty", path, err, broken)
		}
		s.data = map[string]Override{}
	}
	return s, nil
}

// GetAll returns a shallow copy of the overrides map so callers can
// iterate without holding the lock or worrying about concurrent mutation.
func (s *Store) GetAll() map[string]Override {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]Override, len(s.data))
	for k, v := range s.data {
		out[k] = v
	}
	return out
}

// MergeOne validates `partial`, merges it onto the existing entry for
// `plugin` (or onto a zero value if absent), persists, and returns the
// merged result. Returns the validation error directly so HTTP handlers
// can map it to 400.
func (s *Store) MergeOne(plugin string, partial Override) (Override, error) {
	if err := partial.Validate(); err != nil {
		return Override{}, err
	}
	s.mu.Lock()
	prev, hadPrev := s.data[plugin]
	merged := prev.merge(partial)
	// Validate after the merge too: a hand-edited file could have
	// landed an out-of-range value in s.data that the partial doesn't
	// touch, and we don't want a "successful" PUT to confirm a value
	// the editor never set.
	if err := merged.Validate(); err != nil {
		s.mu.Unlock()
		return Override{}, err
	}
	s.data[plugin] = merged
	if err := s.persistLocked(); err != nil {
		// Roll back so the in-memory state matches disk.
		if hadPrev {
			s.data[plugin] = prev
		} else {
			delete(s.data, plugin)
		}
		s.mu.Unlock()
		return Override{}, err
	}
	notify := s.Notify
	s.mu.Unlock()
	s.fireNotify(notify)
	return merged, nil
}

// KeepPosition strips the entry for `plugin` down to just the position
// fields (anchor + offsets), discarding size, opacity, enabled, and
// min/max clamps. Used on install/update where the new manifest may
// have changed sizing semantics in ways that make the old override
// nonsensical, but where the user's chosen screen position is still
// meaningful. Idempotent: a missing entry stays missing; an entry
// without any position fields gets deleted entirely.
func (s *Store) KeepPosition(plugin string) error {
	s.mu.Lock()
	prev, ok := s.data[plugin]
	if !ok {
		s.mu.Unlock()
		return nil
	}
	trimmed := Override{
		Anchor:  prev.Anchor,
		OffsetX: prev.OffsetX,
		OffsetY: prev.OffsetY,
	}
	if trimmed.Anchor == nil && trimmed.OffsetX == nil && trimmed.OffsetY == nil {
		delete(s.data, plugin)
	} else {
		s.data[plugin] = trimmed
	}
	if err := s.persistLocked(); err != nil {
		s.data[plugin] = prev
		s.mu.Unlock()
		return err
	}
	notify := s.Notify
	s.mu.Unlock()
	s.fireNotify(notify)
	return nil
}

// Delete removes the entry for `plugin`. Idempotent: deleting a missing
// entry is not an error.
func (s *Store) Delete(plugin string) error {
	s.mu.Lock()
	prev, ok := s.data[plugin]
	if !ok {
		s.mu.Unlock()
		return nil
	}
	delete(s.data, plugin)
	if err := s.persistLocked(); err != nil {
		// Roll back so in-memory matches disk.
		s.data[plugin] = prev
		s.mu.Unlock()
		return err
	}
	notify := s.Notify
	s.mu.Unlock()
	s.fireNotify(notify)
	return nil
}

// fireNotify invokes the callback with panic protection so a buggy
// subscriber can't tear down the HTTP handler — the persist has
// already committed to disk by this point.
func (s *Store) fireNotify(notify func()) {
	if notify == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[overrides] notify panic: %v", r)
		}
	}()
	notify()
}

// persistLocked writes the full map to disk. Caller must hold s.mu.
// Writes to a temp file and renames to make the swap atomic — a crash
// mid-write leaves the prior file intact rather than producing a
// truncated half-file.
func (s *Store) persistLocked() error {
	raw, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal overrides: %w", err)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0644); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("rename %s -> %s: %w", tmp, s.path, err)
	}
	return nil
}
