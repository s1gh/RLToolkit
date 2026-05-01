package main

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sync"
)

type PluginManifest struct {
	Name        string        `json:"name"`
	Title       string        `json:"title"`
	Version     string        `json:"version"`
	Author      string        `json:"author"`
	Description string        `json:"description,omitempty"`
	Overlay     OverlayConfig `json:"overlay"`
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
}

// PluginManager scans the plugin directory and serves manifest listings.
// Results are cached and only re-scanned when an entry's manifest.json
// mtime changes — a dashboard refresh storm collapses to a few stat()s
// instead of full re-reads.
type PluginManager struct {
	dir string

	mu     sync.Mutex
	cache  map[string]*cachedManifest // key: plugin folder name
	loaded map[string]string          // name@version, for log-once tracking
}

type cachedManifest struct {
	mtime    int64
	manifest *PluginManifest
}

func NewPluginManager(dir string) *PluginManager {
	if err := os.MkdirAll(dir, 0755); err != nil {
		log.Printf("[plugins] Cannot create plugin dir %q: %v", dir, err)
	}
	pm := &PluginManager{
		dir:    dir,
		cache:  make(map[string]*cachedManifest),
		loaded: make(map[string]string),
	}
	pm.List() // initial scan, surfaces what's present at startup
	return pm
}

// List returns the current manifest list. Newly-dropped plugin folders
// appear without a server restart; a refresh of the dashboard is enough.
//
// The mtime cache means repeated calls don't re-parse unchanged manifests,
// and removed folders are detected by absence from the directory listing.
func (pm *PluginManager) List() []*PluginManifest {
	entries, err := os.ReadDir(pm.dir)
	if err != nil {
		log.Printf("[plugins] Cannot read plugin dir %q: %v", pm.dir, err)
		return nil
	}

	pm.mu.Lock()
	defer pm.mu.Unlock()

	out := make([]*PluginManifest, 0, len(entries))
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
			var m PluginManifest
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
