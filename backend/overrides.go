package backend

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
)

// OverlayOverride is the per-user override of a plugin manifest's overlay
// block. Every field is optional — a partial override layers cleanly over
// the manifest defaults via shallow merge on the JS side. Pointers let us
// distinguish "field not set in override" (nil; manifest wins) from "field
// set to its zero value" (e.g., explicit OffsetX=0 still wins over a
// non-zero manifest default).
type OverlayOverride struct {
	Anchor  *string  `json:"anchor,omitempty"`
	OffsetX *int     `json:"offset_x,omitempty"`
	OffsetY *int     `json:"offset_y,omitempty"`
	Width   *int     `json:"width,omitempty"`
	Height  *int     `json:"height,omitempty"`
	Opacity *float64 `json:"opacity,omitempty"`
	// Enabled gates whether the plugin's overlay iframe is mounted at
	// all. Pointer so nil means "no preference; default = true". When
	// explicitly false, /overlay skips the plugin entirely (iframe
	// unmounts, EventSource closes, no event consumption).
	Enabled *bool `json:"enabled,omitempty"`
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

// validate returns an error if any present field carries a disallowed
// value. Absent fields (nil pointers) are always allowed — they mean
// "fall back to the manifest."
func (o *OverlayOverride) validate() error {
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
	if o.Width != nil && (*o.Width < 0 || *o.Width > maxOverlayPixel) {
		return fmt.Errorf("width must be between 0 and %d, got %d", maxOverlayPixel, *o.Width)
	}
	if o.Height != nil && (*o.Height < 0 || *o.Height > maxOverlayPixel) {
		return fmt.Errorf("height must be between 0 and %d, got %d", maxOverlayPixel, *o.Height)
	}
	if o.Opacity != nil && (*o.Opacity < 0 || *o.Opacity > 1) {
		return fmt.Errorf("opacity must be between 0 and 1, got %g", *o.Opacity)
	}
	return nil
}

// merge layers `partial` on top of `o`, leaving `o`'s fields intact when
// the partial omits them. Returns a new value rather than mutating in
// place so callers can't accidentally hold a half-updated reference.
func (o OverlayOverride) merge(partial OverlayOverride) OverlayOverride {
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
	return o
}

// OverridesStore owns data/overlay-overrides.json. The file is loaded once
// at startup and held in memory; writes go through a single mutex and
// flush the whole map back to disk. The file is small (one entry per
// plugin) so a full rewrite per change is fine — no need for an append
// log or per-key fsync.
type OverridesStore struct {
	path string

	mu   sync.RWMutex
	data map[string]OverlayOverride

	// Notify, if set, is called on the goroutine performing the write
	// after every successful persist (MergeOne and Delete). Wired by
	// main to publish a synthetic _OverridesChanged SSE event so
	// production /overlay clients can reflow in place. Not held under
	// any lock — the callback is free to take its own.
	Notify func()
}

// NewOverridesStore loads the existing file (or starts with an empty map
// when the file doesn't exist). On parse failure the corrupt file is
// quarantined to <path>.broken and the store starts empty — the server
// always starts, the user's broken file is preserved for investigation,
// and the editor's first save creates a fresh valid file. Mirrors the
// spec's "production overlay falls back to manifests on corruption"
// guarantee.
func NewOverridesStore(dir string) (*OverridesStore, error) {
	path := filepath.Join(dir, "overlay-overrides.json")
	s := &OverridesStore{
		path: path,
		data: map[string]OverlayOverride{},
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
		s.data = map[string]OverlayOverride{}
	}
	return s, nil
}

// GetAll returns a shallow copy of the overrides map so callers can
// iterate without holding the lock or worrying about concurrent mutation.
func (s *OverridesStore) GetAll() map[string]OverlayOverride {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]OverlayOverride, len(s.data))
	for k, v := range s.data {
		out[k] = v
	}
	return out
}

// MergeOne validates `partial`, merges it onto the existing entry for
// `plugin` (or onto a zero value if absent), persists, and returns the
// merged result. Returns the validation error directly so HTTP handlers
// can map it to 400.
func (s *OverridesStore) MergeOne(plugin string, partial OverlayOverride) (OverlayOverride, error) {
	if err := partial.validate(); err != nil {
		return OverlayOverride{}, err
	}
	s.mu.Lock()
	prev, hadPrev := s.data[plugin]
	merged := prev.merge(partial)
	// Validate after the merge too: a hand-edited file could have
	// landed an out-of-range value in s.data that the partial doesn't
	// touch, and we don't want a "successful" PUT to confirm a value
	// the editor never set.
	if err := merged.validate(); err != nil {
		s.mu.Unlock()
		return OverlayOverride{}, err
	}
	s.data[plugin] = merged
	if err := s.persistLocked(); err != nil {
		// Roll back the in-memory mutation so the API's "save failed"
		// matches the actual store state.
		if hadPrev {
			s.data[plugin] = prev
		} else {
			delete(s.data, plugin)
		}
		s.mu.Unlock()
		return OverlayOverride{}, err
	}
	notify := s.Notify
	s.mu.Unlock()
	s.fireNotify(notify)
	return merged, nil
}

// Delete removes the entry for `plugin`. Idempotent: deleting a missing
// entry is not an error.
func (s *OverridesStore) Delete(plugin string) error {
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

// fireNotify invokes the supplied callback with panic protection so a
// buggy subscriber can't tear down the HTTP handler that triggered the
// write — at that point the persist has already committed to disk and
// the client deserves a clean response, not a torn connection.
func (s *OverridesStore) fireNotify(notify func()) {
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
func (s *OverridesStore) persistLocked() error {
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
