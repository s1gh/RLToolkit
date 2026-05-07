// Package plugins scans the plugin directory and serves manifest
// listings. Results are cached and only re-scanned when an entry's
// manifest.json mtime changes — a dashboard refresh storm collapses
// to a few stat()s instead of full re-reads.
package plugins

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sync"
)

type Manifest struct {
	Name        string        `json:"name"`
	Title       string        `json:"title"`
	Version     string        `json:"version"`
	Author      string        `json:"author"`
	Description string        `json:"description,omitempty"`

	// Overlay is the only required view: it's what the Tauri widget
	// renders on top of the game. Carries sizing/anchoring/visibility
	// gates because that's where they belong (Tauri-only concerns).
	Overlay OverlayConfig `json:"overlay"`

	// Dashboard is an optional full-page browser view. Linked to from
	// the dashboard's per-plugin card; opens in a new tab. Use it for
	// rich UIs that don't fit a transparent overlay (history tables,
	// charts, leaderboards, configuration that's too heavy for a
	// settings panel).
	Dashboard *ViewConfig `json:"dashboard,omitempty"`

	// Settings is an optional iframe-rendered configuration panel. The
	// dashboard exposes a per-plugin "Settings" button when this is
	// set; clicking it opens the file in a modal iframe. The view is
	// responsible for its own layout + RLT.settings.close() wiring.
	Settings *ViewConfig `json:"settings,omitempty"`
}

// ViewConfig points at an HTML file inside the plugin folder. Object
// form (rather than a bare string) so we have room for per-view
// options later without another breaking change.
type ViewConfig struct {
	File string `json:"file"`
}

type OverlayConfig struct {
	File         string  `json:"file"`
	Width        int     `json:"width"`
	Height       int     `json:"height"`
	Anchor       string  `json:"anchor"`
	OffsetX      int     `json:"offset_x"`
	OffsetY      int     `json:"offset_y"`
	Opacity      float64 `json:"opacity"`
	ClickThrough bool    `json:"click_through"`
	// HideWhenUnfocused gates the widget on RL window focus. When true,
	// the SDK default-hides the overlay body at load and toggles inline
	// `display` on each onFocusChange emit. Pointer so absent ≠ false:
	// absent means "the manifest didn't say" and the SDK can apply its
	// current default (false; show always). Explicit false locks that
	// behavior even if the SDK default flips later.
	HideWhenUnfocused *bool `json:"hide_when_unfocused,omitempty"`
	// ShowDuringPhase whitelists lifecycle phases during which the
	// widget is visible. Combines with HideWhenUnfocused via AND: the
	// widget shows only when RL is focused AND the current phase is in
	// the list. Absent (nil) = show in any phase. Empty array would
	// mean "never", so we treat that as the absent case to be safe.
	// Phase strings match LifecyclePhase: none, created, countdown,
	// live, paused, replay, ended, podium.
	ShowDuringPhase []string `json:"show_during_phase,omitempty"`
}

// Manager scans the plugin directory and serves manifest listings.
type Manager struct {
	dir string

	mu     sync.Mutex
	cache  map[string]*cachedManifest // key: plugin folder name
	loaded map[string]string          // name@version, for log-once tracking
}

type cachedManifest struct {
	mtime    int64
	manifest *Manifest
}

// New creates a Manager rooted at dir. The directory is created if
// missing; the initial List call surfaces what's present at startup.
func New(dir string) *Manager {
	if err := os.MkdirAll(dir, 0755); err != nil {
		log.Printf("[plugins] Cannot create plugin dir %q: %v", dir, err)
	}
	pm := &Manager{
		dir:    dir,
		cache:  make(map[string]*cachedManifest),
		loaded: make(map[string]string),
	}
	pm.List()
	return pm
}

// Has reports whether `name` refers to a plugin folder with a valid,
// parseable manifest.json. Used by the file-server middleware to gate
// access — plugins without a manifest aren't loadable, so requests for
// their assets get 404'd instead of silently falling through to disk.
//
// Triggers a refresh of the manifest cache (cheap when nothing
// changed) so a freshly-fixed manifest takes effect on the next
// request without a server restart.
func (pm *Manager) Has(name string) bool {
	for _, m := range pm.List() {
		if m != nil && m.Name == name {
			return true
		}
	}
	return false
}

// List returns the current manifest list. Newly-dropped plugin folders
// appear without a server restart; a refresh of the dashboard is enough.
//
// The mtime cache means repeated calls don't re-parse unchanged manifests,
// and removed folders are detected by absence from the directory listing.
func (pm *Manager) List() []*Manifest {
	entries, err := os.ReadDir(pm.dir)
	if err != nil {
		log.Printf("[plugins] Cannot read plugin dir %q: %v", pm.dir, err)
		return nil
	}

	pm.mu.Lock()
	defer pm.mu.Unlock()

	out := make([]*Manifest, 0, len(entries))
	seen := make(map[string]struct{}, len(entries))
	var logLines []string

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		manifestPath := filepath.Join(pm.dir, name, "manifest.json")
		st, err := os.Stat(manifestPath)
		if err != nil {
			continue
		}
		mtime := st.ModTime().UnixNano()

		cached, ok := pm.cache[name]
		if !ok || cached.mtime != mtime {
			data, err := os.ReadFile(manifestPath)
			if err != nil {
				continue
			}
			var m Manifest
			if err := json.Unmarshal(data, &m); err != nil {
				log.Printf("[plugins] Bad manifest in %s: %v", name, err)
				continue
			}
			// A missing/zero opacity means "fully opaque" by convention.
			// Plugins that want truly invisible should set a small ε.
			if m.Overlay.Opacity == 0 {
				m.Overlay.Opacity = 1.0
			}
			cached = &cachedManifest{mtime: mtime, manifest: &m}
			pm.cache[name] = cached
		}

		out = append(out, cached.manifest)
		seen[cached.manifest.Name] = struct{}{}

		key := cached.manifest.Name + "@" + cached.manifest.Version
		if pm.loaded[cached.manifest.Name] != key {
			logLines = append(logLines,
				"[plugins] Loaded: "+cached.manifest.Title+" v"+cached.manifest.Version+" ("+cached.manifest.Name+")")
			pm.loaded[cached.manifest.Name] = key
		}
	}

	// Detect plugins removed since the last scan and forget their cache.
	for name := range pm.loaded {
		if _, still := seen[name]; !still {
			logLines = append(logLines, "[plugins] Removed: "+name)
			delete(pm.loaded, name)
		}
	}
	for folder := range pm.cache {
		if _, still := seen[pm.cache[folder].manifest.Name]; !still {
			delete(pm.cache, folder)
		}
	}

	for _, line := range logLines {
		log.Println(line)
	}
	return out
}
