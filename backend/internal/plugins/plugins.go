// Package plugins scans the plugin directory and serves manifest
// listings. Results are cached and only re-scanned when an entry's
// manifest.json mtime changes — a dashboard refresh storm collapses
// to a few stat()s instead of full re-reads.
package plugins

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"rl-toolkit/backend/internal/bus"
	"strings"
	"sync"
)

type Manifest struct {
	Name        string        `json:"name"`
	Title       string        `json:"title"`
	Version     string        `json:"version"`
	Author      string        `json:"author"`
	Description string        `json:"description,omitempty"`

	// Overlay is the required view rendered by the Tauri widget on top
	// of the game.
	Overlay OverlayConfig `json:"overlay"`

	// Dashboard is an optional full-page browser view linked from the
	// dashboard's per-plugin card (history tables, charts, leaderboards).
	Dashboard *ViewConfig `json:"dashboard,omitempty"`

	// Settings is an optional iframe-rendered configuration panel
	// surfaced as a "Settings" button on the dashboard. The view is
	// responsible for its own layout + RLT.settings.close() wiring.
	Settings *ViewConfig `json:"settings,omitempty"`

	// Background is an optional always-running view loaded as a hidden
	// iframe in the launcher window for as long as the toolkit is up
	// and the plugin is enabled. Use it for work that has to react to
	// events independent of which UI surface (overlay / settings) the
	// user has open — e.g. a replay uploader that must run regardless
	// of whether the user has the dashboard tab focused.
	Background *ViewConfig `json:"background,omitempty"`

	// Permissions optionally declares relaxations of the per-plugin
	// sandbox. Today the only supported permission is `connect`, which
	// allowlists external https origins the plugin can reach via the
	// /api/plugin-fetch/<name> backend proxy. Used by plugins that talk
	// to third-party APIs (e.g. ballchasing.com) which don't return
	// CORS headers and so can't be called from the iframe directly.
	Permissions *Permissions `json:"permissions,omitempty"`
}

// ViewConfig points at an HTML file inside the plugin folder.
type ViewConfig struct {
	File string `json:"file"`
}

// Permissions declares optional relaxations of the per-plugin
// sandbox. Each field is a tightly-scoped allowlist; defaults stay
// strict.
type Permissions struct {
	// Connect is a list of https origins the plugin is allowed to
	// reach via /api/plugin-fetch/<name>. Each entry must be a bare
	// https origin (scheme + host, optional :443) with no path,
	// query, or fragment. Invalid entries are dropped at manifest
	// load time.
	Connect []string `json:"connect,omitempty"`
}

type OverlayConfig struct {
	File    string  `json:"file"`
	Width   int     `json:"width"`
	Height  int     `json:"height"`
	Anchor  string  `json:"anchor"`
	OffsetX int     `json:"offset_x"`
	OffsetY int     `json:"offset_y"`
	Opacity float64 `json:"opacity"`
	// click_through used to be a per-plugin manifest field. Removed in
	// favor of unconditional click-through in overlay.html: the Tauri
	// overlay window is OS-level click-through anyway, and the overlay
	// editor / OBS browser source render through their own paths, so
	// the manifest setting was redundant in every shipping context.
	// Existing manifests with the field still parse; the value is
	// dropped on read.

	// HideWhenUnfocused gates the widget on RL window focus. Pointer so
	// absent means "no preference; SDK default applies".
	HideWhenUnfocused *bool `json:"hide_when_unfocused,omitempty"`
	// ShowDuringPhase whitelists lifecycle phases during which the
	// widget is visible. ANDs with HideWhenUnfocused. Nil or empty =
	// show in any phase. Phase strings match LifecyclePhase: none,
	// lobby, countdown, live, paused, replay, ended, podium.
	ShowDuringPhase []string `json:"show_during_phase,omitempty"`
	// UnmountOutsidePhase upgrades ShowDuringPhase from a visual gate
	// (display:none on body) to a hard-kill: the iframe is removed
	// from the DOM outside the listed phases, the plugin's SSE
	// subscription dies, and the shared bus filter shrinks. Re-mounted
	// when a listed phase becomes active again. No effect if
	// ShowDuringPhase is empty. Useful for sound-only plugins or any
	// plugin that holds heavy state you'd rather release between
	// phases. Default false (keep iframe mounted, gate visually).
	UnmountOutsidePhase bool `json:"unmount_outside_phase,omitempty"`
}

// validateManifest enforces structural rules and applies defaults
// (zero opacity → 1.0, empty title → name). pluginRoot is used for
// existence checks and to refuse `..`-style traversal in view paths.
// Returns on first error; callers log and skip the plugin.
func validateManifest(m *Manifest, pluginRoot string) error {
	if m == nil {
		return errors.New("nil manifest")
	}
	if strings.TrimSpace(m.Name) == "" {
		return errors.New("name is required")
	}
	if strings.TrimSpace(m.Version) == "" {
		return errors.New("version is required")
	}
	if strings.TrimSpace(m.Overlay.File) == "" {
		return errors.New("overlay.file is required")
	}
	if err := validateViewFile("overlay.file", m.Overlay.File, pluginRoot); err != nil {
		return err
	}
	if m.Dashboard != nil {
		if strings.TrimSpace(m.Dashboard.File) == "" {
			return errors.New("dashboard.file is required when dashboard is set")
		}
		if err := validateViewFile("dashboard.file", m.Dashboard.File, pluginRoot); err != nil {
			return err
		}
	}
	if m.Settings != nil {
		if strings.TrimSpace(m.Settings.File) == "" {
			return errors.New("settings.file is required when settings is set")
		}
		if err := validateViewFile("settings.file", m.Settings.File, pluginRoot); err != nil {
			return err
		}
	}
	if m.Background != nil {
		if strings.TrimSpace(m.Background.File) == "" {
			return errors.New("background.file is required when background is set")
		}
		if err := validateViewFile("background.file", m.Background.File, pluginRoot); err != nil {
			return err
		}
	}
	if m.Permissions != nil {
		m.Permissions.Connect = sanitizeConnectPermissions(m.Name, m.Permissions.Connect)
	}

	if strings.TrimSpace(m.Title) == "" {
		m.Title = m.Name
	}
	// Zero opacity is treated as fully opaque by convention; truly
	// invisible plugins should set a small ε.
	if m.Overlay.Opacity == 0 {
		m.Overlay.Opacity = 1.0
	}
	return nil
}

// validateViewFile checks that a manifest-declared view file:
//   - exists on disk
//   - resolves inside the plugin folder (refuses `..` traversal and
//     absolute paths that would escape the sandbox)
func validateViewFile(field, rel, pluginRoot string) error {
	if filepath.IsAbs(rel) {
		return fmt.Errorf("%s must be a relative path inside the plugin folder", field)
	}
	full := filepath.Join(pluginRoot, rel)
	rootAbs, err := filepath.Abs(pluginRoot)
	if err != nil {
		return fmt.Errorf("%s: cannot resolve plugin root: %w", field, err)
	}
	fullAbs, err := filepath.Abs(full)
	if err != nil {
		return fmt.Errorf("%s: cannot resolve path: %w", field, err)
	}
	rel2, err := filepath.Rel(rootAbs, fullAbs)
	if err != nil || strings.HasPrefix(rel2, "..") {
		return fmt.Errorf("%s %q escapes the plugin folder", field, rel)
	}
	if _, err := os.Stat(fullAbs); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%s %q does not exist", field, rel)
		}
		return fmt.Errorf("%s %q: %w", field, rel, err)
	}
	return nil
}

// Broadcaster is the slim publish surface used here (matches
// state.Broadcaster).
type Broadcaster interface {
	Broadcast(evt bus.Event)
}

// Manager scans the plugin directory and serves manifest listings.
// Dev plugins (in-memory <name → absolute folder path>) shadow
// installed plugins of the same name in List() output, mtime-cached
// like installed plugins. This is how `rl-toolkit dev` hot-loads
// without touching disk.
type Manager struct {
	dir string

	mu     sync.Mutex
	cache  map[string]*cachedManifest // key: plugin folder name (installed only)
	loaded map[string]string          // name@version, for log-once tracking

	// Dev plugins: name → absolute folder path.
	dev map[string]string
	// devCache is keyed by absolute folder path so re-registering the
	// same path under a different name doesn't lose the cache.
	devCache map[string]*cachedManifest

	bus Broadcaster
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
		dir:      dir,
		cache:    make(map[string]*cachedManifest),
		loaded:   make(map[string]string),
		dev:      make(map[string]string),
		devCache: make(map[string]*cachedManifest),
	}
	pm.List()
	return pm
}

// Has reports whether `name` refers to a plugin with a valid,
// parseable manifest.json. Used by the file-server middleware to gate
// access. Triggers a manifest cache refresh so a freshly-fixed
// manifest takes effect without a server restart.
func (pm *Manager) Has(name string) bool {
	for _, m := range pm.List() {
		if m != nil && m.Name == name {
			return true
		}
	}
	return false
}

// Get returns the manifest for `name`, or nil if no plugin folder by
// that name has a valid manifest. Triggers a manifest cache refresh.
func (pm *Manager) Get(name string) *Manifest {
	for _, m := range pm.List() {
		if m != nil && m.Name == name {
			return m
		}
	}
	return nil
}

// List returns the current manifest list. Newly-dropped plugin folders
// appear without a server restart. The mtime cache skips re-parsing
// unchanged manifests; removed folders drop out of the listing.
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
				delete(pm.cache, name)
				continue
			}
			pluginRoot := filepath.Join(pm.dir, name)
			if err := validateManifest(&m, pluginRoot); err != nil {
				log.Printf("[plugins] Invalid manifest in %s: %v", name, err)
				delete(pm.cache, name)
				continue
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

	// Dev-plugin overlay: parse each registered dev manifest and
	// shadow any installed plugin with the same name.
	for name, path := range pm.dev {
		manifestPath := filepath.Join(path, "manifest.json")
		st, err := os.Stat(manifestPath)
		if err != nil {
			log.Printf("[plugins] Dev %s: cannot stat manifest at %s: %v", name, manifestPath, err)
			continue
		}
		mtime := st.ModTime().UnixNano()
		cached, ok := pm.devCache[path]
		if !ok || cached.mtime != mtime {
			data, err := os.ReadFile(manifestPath)
			if err != nil {
				continue
			}
			var m Manifest
			if err := json.Unmarshal(data, &m); err != nil {
				log.Printf("[plugins] Dev %s: bad manifest: %v", name, err)
				delete(pm.devCache, path)
				continue
			}
			if err := validateManifest(&m, path); err != nil {
				log.Printf("[plugins] Dev %s: invalid manifest: %v", name, err)
				delete(pm.devCache, path)
				continue
			}
			cached = &cachedManifest{mtime: mtime, manifest: &m}
			pm.devCache[path] = cached
		}

		// Replace if same name already in `out`, else append.
		replaced := false
		for i, existing := range out {
			if existing != nil && existing.Name == cached.manifest.Name {
				out[i] = cached.manifest
				replaced = true
				break
			}
		}
		if !replaced {
			out = append(out, cached.manifest)
		}
		seen[cached.manifest.Name] = struct{}{}

		key := "dev:" + cached.manifest.Name + "@" + cached.manifest.Version
		if pm.loaded[cached.manifest.Name] != key {
			logLines = append(logLines,
				"[plugins] Dev loaded: "+cached.manifest.Title+" v"+cached.manifest.Version+" ("+cached.manifest.Name+") from "+path)
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

// RegisterDev marks `path` as the active source for plugin `name`. The
// path must contain a parseable manifest.json; if not, the call fails
// and no registration is recorded. A subsequent List() call will
// surface this plugin in place of any installed plugin with the same
// name. Re-registering the same name replaces the previous path.
func (pm *Manager) RegisterDev(name, path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	manifestPath := filepath.Join(abs, "manifest.json")
	if _, err := os.Stat(manifestPath); err != nil {
		return err
	}

	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.dev[name] = abs
	delete(pm.devCache, abs)
	return nil
}

// UnregisterDev removes the dev registration for `name`. Subsequent
// List() calls will revert to whatever installed plugin (if any) sits
// at <pluginsDir>/<name>/.
func (pm *Manager) UnregisterDev(name string) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	if path, ok := pm.dev[name]; ok {
		delete(pm.devCache, path)
		delete(pm.dev, name)
	}
}

// DevPath returns the absolute folder path for a dev-registered plugin
// name, or "" if no dev registration exists. Used by the file-server
// middleware so requests for /plugins/<name>/foo.js read from the dev
// folder when one is registered.
func (pm *Manager) DevPath(name string) string {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	return pm.dev[name]
}

// DevNames returns the names of every dev-registered plugin. Order is
// unspecified; callers that need stability should sort. Used by the
// plugin catalog to exclude dev plugins from update diffs.
func (pm *Manager) DevNames() []string {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	out := make([]string, 0, len(pm.dev))
	for name := range pm.dev {
		out = append(out, name)
	}
	return out
}

// AttachBroadcaster wires a publisher so dev-reload notifications
// reach SSE subscribers. Optional — without it, NotifyReload degrades
// to "log + invalidate cache".
func (pm *Manager) AttachBroadcaster(b Broadcaster) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.bus = b
}

// NotifyReload signals that a dev-registered plugin's assets have
// changed. Invalidates the dev manifest cache so the next List()
// re-reads it, then publishes `_DevPluginReload` over the bus (if
// attached) so any open SDK instance of this plugin reloads its page.
func (pm *Manager) NotifyReload(name string) {
	pm.mu.Lock()
	if path, ok := pm.dev[name]; ok {
		delete(pm.devCache, path)
	}
	b := pm.bus
	pm.mu.Unlock()

	log.Printf("[plugins] Dev reload: %s", name)
	if b != nil {
		body, err := json.Marshal(map[string]string{"name": name})
		if err == nil {
			b.Broadcast(bus.Event{Name: "_DevPluginReload", Data: body})
		}
	}
}

// sanitizeConnectPermissions filters a Permissions.Connect list down
// to entries that are valid bare https origins. Each dropped entry
// is logged once with the plugin name so authors can spot typos.
//
// Valid entry rules:
//   - Parses cleanly via url.Parse.
//   - Scheme is exactly "https".
//   - Host is non-empty.
//   - No userinfo.
//   - Path is empty or "/".
//   - No query, no fragment.
//
// A port is allowed but optional; the proxy compares scheme+host+port
// strings between manifest entry and target URL, so a manifest that
// says `https://api.example.com` matches `https://api.example.com/x`
// (Host "api.example.com") and a manifest that says
// `https://api.example.com:8443` matches `https://api.example.com:8443/x`.
func sanitizeConnectPermissions(pluginName string, in []string) []string {
	out := make([]string, 0, len(in))
	for _, raw := range in {
		if isValidConnectOrigin(raw) {
			out = append(out, raw)
		} else {
			log.Printf("[plugins] %s: dropped invalid connect permission %q", pluginName, raw)
		}
	}
	return out
}

func isValidConnectOrigin(raw string) bool {
	if raw == "" {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	if u.Scheme != "https" {
		return false
	}
	if u.Host == "" {
		return false
	}
	if u.User != nil {
		return false
	}
	if u.Path != "" && u.Path != "/" {
		return false
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	return true
}
