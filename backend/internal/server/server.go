// Package server wires HTTP handlers + SSE fan-out to the Bus,
// DataStore, PluginManager, and RLSource. Long-lived per-request
// goroutines (notably handleSSE) listen on r.Context(); the package
// itself owns no goroutines.
package server

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"rl-toolkit/backend/internal/bootid"
	"rl-toolkit/backend/internal/bus"
	"rl-toolkit/backend/internal/catalog"
	"rl-toolkit/backend/internal/datastore"
	"rl-toolkit/backend/internal/discoveries"
	"rl-toolkit/backend/internal/emit"
	"rl-toolkit/backend/internal/identity"
	"rl-toolkit/backend/internal/install"
	"rl-toolkit/backend/internal/overrides"
	"rl-toolkit/backend/internal/plugins"
	"rl-toolkit/backend/internal/roster"
	"rl-toolkit/backend/internal/source"
	"rl-toolkit/backend/internal/state"
	"rl-toolkit/backend/internal/types"
	"strings"
	"sync"
	"time"
)

// Tunables. The numbers are load-bearing: a stalled SSE write
// eventually fills the bus's per-subscriber slot, and on Linux/Proton
// that backpressure can reach RL's send() and freeze the game thread.
// Better to disconnect a stuck client than to let any link in the
// chain block.
const (
	sseHeartbeat     = 15 * time.Second
	sseWriteDeadline = 2 * time.Second

	// MaxPluginValueBytes caps a single Set body to keep a misbehaving
	// plugin from filling memory or disk.
	MaxPluginValueBytes = 10 << 20 // 10 MiB
)

// Deps bundles every long-lived dependency the server reads from. Made
// a struct (instead of positional args) so the call site stays
// readable as the list grows.
type Deps struct {
	Bus         *bus.Bus
	Store       *datastore.Store
	Plugins     *plugins.Manager
	Source      *source.RL
	MatchState  *state.MatchState
	Roster      *roster.Tracker
	Demos       *emit.Demos
	Overrides   *overrides.Store
	Discoveries *discoveries.Store
	Identity    *identity.Store
	PluginDir   string
}

// Server holds the wiring between HTTP routes and the live runtime
// objects. Construct with New; serve via Routes().
type Server struct {
	deps Deps
}

// New returns a Server bound to deps.
func New(deps Deps) *Server { return &Server{deps: deps} }

// Routes returns the configured http.Handler with CORS applied.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/events", s.handleSSE)
	mux.HandleFunc("/api/data/", s.handleData)
	mux.HandleFunc("/api/plugins", s.handlePlugins)
	mux.HandleFunc("/api/events", s.handleEventCatalog)
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/match-state", s.handleMatchState)
	mux.HandleFunc("/api/statfeed-discoveries", s.handleStatfeedDiscoveries)
	mux.HandleFunc("/api/metrics", s.handleMetrics)
	mux.HandleFunc("/api/boot-id", s.handleBootID)
	mux.HandleFunc("/api/identity", s.handleIdentity)
	mux.HandleFunc("/api/overlay/overrides", s.handleOverlayOverridesAll)
	mux.HandleFunc("/api/overlay/overrides/", s.handleOverlayOverridesOne)
	mux.HandleFunc("/api/sideload", s.handleSideload)
	mux.HandleFunc("/overlay", s.handleOverlay)
	mux.HandleFunc("/sdk.js", s.handleSDKJS)
	mux.HandleFunc("/sdk.css", s.handleSDKCSS)
	mux.HandleFunc("/fonts/", s.handleFont)
	mux.HandleFunc("/overlay-editor.js", s.handleOverlayEditorJS)
	mux.HandleFunc("/favicon.ico", s.handleFavicon)
	// Plugin assets are served straight from disk so iterating on a
	// plugin is one-edit-then-refresh. Two layers of middleware sit in
	// front:
	//
	//   - requireManifest gates every request on the plugin having a
	//     valid manifest.json. A folder without (or with a malformed)
	//     manifest is invisible to the dashboard and the unified
	//     overlay; serving its files would produce a half-loaded
	//     plugin that runs in isolation but isn't reachable through
	//     the rest of the toolkit. Better to fail fast at the HTTP
	//     layer with a clear 404.
	//
	//   - noCache forces revalidation so plugin edits show up on the
	//     next request. Browsers (and the rl-widget webview in
	//     particular) apply a heuristic freshness window based on
	//     Last-Modified that's much too long during plugin
	//     development. "no-cache" means "always revalidate", not
	//     "don't cache" — unchanged files still come back cheaply as
	//     a 304.
	mux.Handle("/plugins/", http.StripPrefix("/plugins/",
		s.requireManifest(noCache(http.HandlerFunc(s.servePluginAsset)))))
	mux.HandleFunc("/", s.handleDashboard)

	return cors(mux)
}

// noCache wraps a handler so its responses always carry
// `Cache-Control: no-cache`. Used for files that change as the user
// iterates (plugin assets) and for the embedded HTML shells. Despite
// the name, "no-cache" doesn't disable caching — it forces
// revalidation, so unchanged files still come back cheaply as a 304.
func noCache(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		h.ServeHTTP(w, r)
	})
}

// requireManifest blocks plugin asset requests when the plugin's
// manifest is missing or malformed. The PluginManager scans manifests
// at module-load time and on each List() call, and surfaces parse
// errors via "[plugins] Bad manifest in <name>: ..." log lines. We
// gate the file server on the same liveness check so a broken plugin
// can't half-load.
func (s *Server) requireManifest(inner http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name, _, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
		if name == "" || !s.deps.Plugins.Has(name) {
			http.NotFound(w, r)
			return
		}
		inner.ServeHTTP(w, r)
	})
}

// servePluginAsset serves /plugins/<name>/<rest> from the dev folder
// if a dev plugin named <name> is registered, otherwise from the
// installed plugins directory. The leading /plugins/ has already been
// stripped by the time this handler runs, so r.URL.Path looks like
// "<name>" or "<name>/<rest>".
//
// We can't use http.FileServer here because the directory root is
// per-request: a dev plugin's assets live in an arbitrary folder on
// the developer's machine, while installed plugins live under
// s.deps.PluginDir.
func (s *Server) servePluginAsset(w http.ResponseWriter, r *http.Request) {
	rel := strings.TrimPrefix(r.URL.Path, "/")
	name, rest, _ := strings.Cut(rel, "/")
	if name == "" {
		http.NotFound(w, r)
		return
	}

	root := s.deps.PluginDir
	if devPath := s.deps.Plugins.DevPath(name); devPath != "" {
		// Dev plugin registered: serve from the dev folder. The folder
		// already contains manifest.json + assets at its root, so we
		// only need the part after <name>/.
		s.serveFromRoot(w, r, devPath, rest)
		return
	}
	s.serveFromRoot(w, r, filepath.Join(root, name), rest)
}

// handleSideload accepts a multipart upload of a .rltp file under the
// form field "file" and installs it via install.Install. Used by the
// dashboard's drag-drop UI; also reachable via curl for testing.
//
// Cap on body size is intentional: a runaway upload should fail fast,
// not consume disk and memory until OOM. 64 MiB is generous for plugin
// archives (today's largest in-tree plugin is well under 100 KiB).
func (s *Server) handleSideload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	const maxBytes = 64 << 20
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		http.Error(w, "bad multipart: "+err.Error(), http.StatusBadRequest)
		return
	}
	file, hdr, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "missing 'file' field", http.StatusBadRequest)
		return
	}
	defer file.Close()
	if !strings.HasSuffix(strings.ToLower(hdr.Filename), ".rltp") {
		http.Error(w, "file must end in .rltp", http.StatusBadRequest)
		return
	}

	tmp, err := os.CreateTemp("", "sideload-*.rltp")
	if err != nil {
		http.Error(w, "tmp file: "+err.Error(), http.StatusInternalServerError)
		return
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if _, err := io.Copy(tmp, file); err != nil {
		tmp.Close()
		http.Error(w, "write tmp: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := tmp.Close(); err != nil {
		http.Error(w, "close tmp: "+err.Error(), http.StatusInternalServerError)
		return
	}

	name, err := install.Install(tmpPath, s.deps.PluginDir)
	if err != nil {
		http.Error(w, "install: "+err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"name": name})
}

// serveFromRoot serves `rest` from `root`, refusing path traversal and
// directory listings. Empty `rest` is treated as a 404 — plugin asset
// requests are always for a specific file, never a directory listing.
func (s *Server) serveFromRoot(w http.ResponseWriter, r *http.Request, root, rest string) {
	if rest == "" {
		http.NotFound(w, r)
		return
	}
	cleaned := filepath.Clean(rest)
	if cleaned == "." || strings.HasPrefix(cleaned, "..") || strings.Contains(cleaned, ".."+string(filepath.Separator)) {
		http.NotFound(w, r)
		return
	}
	abs := filepath.Join(root, cleaned)
	relCheck, err := filepath.Rel(root, abs)
	if err != nil || strings.HasPrefix(relCheck, "..") {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, abs)
}

// cors permits any origin: the server is meant to be reached from
// arbitrary local pages (overlays in OBS, dashboard, plugin dev
// servers) and is loopback-bound in practice.
func cors(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET,POST,PUT,DELETE,OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		h.ServeHTTP(w, r)
	})
}

// ── shared response helpers ─────────────────────────────────

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("[http] write json: %v", err)
	}
}

func writeAsset(w http.ResponseWriter, contentType, cacheControl string, body []byte) {
	w.Header().Set("Content-Type", contentType)
	if cacheControl != "" {
		w.Header().Set("Cache-Control", cacheControl)
	}
	_, _ = w.Write(body)
}

// httpError logs the underlying error and returns a generic message to
// the client so internal details (paths, parser output) don't leak.
func httpError(w http.ResponseWriter, ctx string, err error, status int) {
	log.Printf("[http] %s: %v", ctx, err)
	http.Error(w, http.StatusText(status), status)
}

// ── SSE ─────────────────────────────────────────────────────

var (
	sseDataPrefix = []byte("data: ")
	sseRecordEnd  = []byte("\n\n")
	ssePingFrame  = []byte(": ping\n\n")
	sseFramePool  = sync.Pool{New: func() any { return bytes.NewBuffer(make([]byte, 0, 512)) }}
)

// parseEventsFilter turns a comma-separated `?events=A,B,C` query
// value into a set the bus can check per-publish. Empty input → nil
// (no filter, all events delivered). Whitespace and empty entries are
// trimmed; the synthetic "_*" framing events bypass the filter on the
// bus side, so callers don't need to add them.
func parseEventsFilter(raw string) map[string]struct{} {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	out := make(map[string]struct{})
	for _, name := range strings.Split(raw, ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		out[name] = struct{}{}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (s *Server) handleSSE(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	filter := parseEventsFilter(r.URL.Query().Get("events"))
	ch, cancel := s.deps.Bus.Subscribe(filter)
	defer cancel()
	wantsEvent := func(name string) bool {
		if filter == nil {
			return true
		}
		_, ok := filter[name]
		return ok
	}

	rc := http.NewResponseController(w)
	writeFrame := func(prefix, body, suffix []byte) bool {
		_ = rc.SetWriteDeadline(time.Now().Add(sseWriteDeadline))
		buf := sseFramePool.Get().(*bytes.Buffer)
		buf.Reset()
		buf.Write(prefix)
		if len(body) > 0 {
			buf.Write(body)
		}
		if len(suffix) > 0 {
			buf.Write(suffix)
		}
		_, err := w.Write(buf.Bytes())
		sseFramePool.Put(buf)
		if err != nil {
			return false
		}
		return rc.Flush() == nil
	}

	// First-frame: ship the process boot ID so plugins can detect a
	// launcher restart and reset per-session state.
	{
		bootFrame := []byte(`{"Event":"_BootId","bootId":"` + bootid.Get() + `"}`)
		if !writeFrame(sseDataPrefix, bootFrame, sseRecordEnd) {
			return
		}
	}
	if !writeFrame(sseDataPrefix, source.StatusEventBytes(s.deps.Source.Status()), sseRecordEnd) {
		return
	}
	// Initial match-state snapshot.
	if s.deps.MatchState != nil {
		snap := s.deps.MatchState.Snapshot()
		body, mErr := json.Marshal(snap)
		if mErr == nil {
			envelope, eErr := json.Marshal(struct {
				Event string          `json:"Event"`
				Data  json.RawMessage `json:"Data"`
			}{Event: "_MatchState", Data: body})
			if eErr == nil {
				if !writeFrame(sseDataPrefix, envelope, sseRecordEnd) {
					return
				}
			}
		}
	}
	// Initial identity snapshot.
	if s.deps.Identity != nil {
		body, err := json.Marshal(s.deps.Identity.Get())
		if err == nil {
			envelope, err := json.Marshal(struct {
				Event string          `json:"Event"`
				Data  json.RawMessage `json:"Data"`
			}{Event: "_IdentityChanged", Data: body})
			if err == nil {
				if !writeFrame(sseDataPrefix, envelope, sseRecordEnd) {
					return
				}
			}
		}
	}
	// Replay the cached roster so a plugin refreshing mid-match (or
	// connecting after the most recent roster delta) sees the current
	// player list immediately.
	if s.deps.Roster != nil {
		if init := s.deps.Roster.Snapshot(); init != nil {
			if !writeFrame(sseDataPrefix, init, sseRecordEnd) {
				return
			}
		}
	}
	// Replay this match's _PlayerDemolished history.
	if s.deps.Demos != nil && wantsEvent("_PlayerDemolished") {
		for _, raw := range s.deps.Demos.DemolishLog() {
			if !writeFrame(sseDataPrefix, raw, sseRecordEnd) {
				return
			}
		}
	}

	hb := time.NewTicker(sseHeartbeat)
	defer hb.Stop()

	for {
		select {
		case msg, ok := <-ch:
			if !ok {
				return
			}
			if !writeFrame(sseDataPrefix, msg, sseRecordEnd) {
				return
			}
		case <-hb.C:
			if !writeFrame(ssePingFrame, nil, nil) {
				return
			}
		case <-r.Context().Done():
			return
		}
	}
}

// ── Data REST API ───────────────────────────────────────────

func (s *Server) handleData(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/data/")
	plugin, key, _ := strings.Cut(path, "/")
	if plugin == "" {
		http.Error(w, "plugin name required", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodGet:
		s.handleDataGet(w, plugin, key)
	case http.MethodPost, http.MethodPut:
		s.handleDataWrite(w, r, plugin, key)
	case http.MethodDelete:
		s.handleDataDelete(w, plugin, key)
	default:
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleDataGet(w http.ResponseWriter, plugin, key string) {
	if key == "" {
		all, err := s.deps.Store.LoadAll(plugin)
		if err != nil {
			httpError(w, "load all", err, http.StatusInternalServerError)
			return
		}
		writeJSON(w, all)
		return
	}
	val, ok, err := s.deps.Store.Get(plugin, key)
	if err != nil {
		httpError(w, "get", err, http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(val)
}

func (s *Server) handleDataWrite(w http.ResponseWriter, r *http.Request, plugin, key string) {
	if key == "" {
		http.Error(w, "key required", http.StatusBadRequest)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, MaxPluginValueBytes))
	if err != nil {
		httpError(w, "read body", err, http.StatusInternalServerError)
		return
	}
	if err := s.deps.Store.Set(plugin, key, json.RawMessage(body)); err != nil {
		httpError(w, "set", err, http.StatusInternalServerError)
		return
	}
	s.broadcastStoreChanged(plugin, key, "set")
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDataDelete(w http.ResponseWriter, plugin, key string) {
	if key == "" {
		http.Error(w, "key required", http.StatusBadRequest)
		return
	}
	if err := s.deps.Store.Delete(plugin, key); err != nil {
		httpError(w, "delete", err, http.StatusInternalServerError)
		return
	}
	s.broadcastStoreChanged(plugin, key, "delete")
	w.WriteHeader(http.StatusNoContent)
}

// broadcastStoreChanged emits _StoreChanged so other live SDK contexts
// (overlay, dashboard preview, settings panel) can react when a key in
// their namespace is written from somewhere else. The SDK filters by
// namespace inside RLT.store.onChange so plugins only see their own
// changes — but we broadcast unfiltered because any subscriber could
// own any namespace, and the per-namespace filter is cheap on the JS
// side.
func (s *Server) broadcastStoreChanged(namespace, key, op string) {
	if s.deps.Bus == nil {
		return
	}
	body, err := json.Marshal(struct {
		Namespace string `json:"namespace"`
		Key       string `json:"key"`
		Op        string `json:"op"`
	}{Namespace: namespace, Key: key, Op: op})
	if err != nil {
		return
	}
	s.deps.Bus.Broadcast(bus.Event{Name: "_StoreChanged", Data: body})
}

func (s *Server) handlePlugins(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, s.deps.Plugins.List())
}

func (s *Server) handleEventCatalog(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, catalog.Entries)
}

func (s *Server) handleStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]source.Status{"rl_api": s.deps.Source.Status()})
}

// handleMatchState returns the current authoritative MatchState
// snapshot. Useful for poll-based consumers that don't subscribe to
// SSE — dashboards, eww/ags widgets, the desktop widget supervisor,
// etc.
func (s *Server) handleMatchState(w http.ResponseWriter, _ *http.Request) {
	if s.deps.MatchState == nil {
		writeJSON(w, state.Snapshot{Phase: types.PhaseNone, PreviousPhase: types.PhaseNone, Since: time.Now(), Trigger: "initial"})
		return
	}
	writeJSON(w, s.deps.MatchState.Snapshot())
}

// handleStatfeedDiscoveries serves the persistent registry of unknown
// Statfeed event names so curl users can browse what RL has emitted
// that isn't in the verified registry yet.
func (s *Server) handleStatfeedDiscoveries(w http.ResponseWriter, _ *http.Request) {
	if s.deps.Discoveries == nil {
		writeJSON(w, []*discoveries.Entry{})
		return
	}
	writeJSON(w, s.deps.Discoveries.All())
}

// handleMetrics exposes the event-bus telemetry: subscriber count,
// publish/delivery counters, eviction count, throughput, and publish
// latency percentiles over a recent window.
func (s *Server) handleMetrics(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, s.deps.Bus.Metrics())
}

// ── Static / embedded assets ────────────────────────────────

func (s *Server) handleOverlay(w http.ResponseWriter, _ *http.Request) {
	writeAsset(w, "text/html; charset=utf-8", "no-cache", overlayHTML)
}

func (s *Server) handleSDKJS(w http.ResponseWriter, _ *http.Request) {
	writeAsset(w, "application/javascript; charset=utf-8", "no-cache", sdkJSBytes)
}

func (s *Server) handleSDKCSS(w http.ResponseWriter, _ *http.Request) {
	writeAsset(w, "text/css; charset=utf-8", "no-cache", sdkCSSBytes)
}

// handleFont serves bundled woff2 font files from the embed FS. Path
// is /fonts/<name>.woff2; only that exact suffix is allowed so a path
// like /fonts/../sdk.js can't escape into other embedded assets. Long
// Cache-Control because the bytes are content-addressed (file names
// map to specific Google Fonts revisions); a font swap would land via
// a rename, busting the cache.
func (s *Server) handleFont(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/fonts/")
	if name == "" || strings.ContainsAny(name, "/\\") || !strings.HasSuffix(name, ".woff2") {
		http.NotFound(w, r)
		return
	}
	body, err := webFS.ReadFile("web/fonts/" + name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	writeAsset(w, "font/woff2", "public, max-age=31536000, immutable", body)
}

func (s *Server) handleOverlayEditorJS(w http.ResponseWriter, _ *http.Request) {
	writeAsset(w, "application/javascript; charset=utf-8", "no-cache", overlayEditorJS)
}

func (s *Server) handleFavicon(w http.ResponseWriter, _ *http.Request) {
	writeAsset(w, "image/svg+xml", "public, max-age=86400", faviconSVGBytes)
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	writeAsset(w, "text/html; charset=utf-8", "no-cache", dashboardHTML)
}

// ── Overlay overrides API ───────────────────────────────────

func (s *Server) handleOverlayOverridesAll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, s.deps.Overrides.GetAll())
}

func (s *Server) handleOverlayOverridesOne(w http.ResponseWriter, r *http.Request) {
	plugin := strings.TrimPrefix(r.URL.Path, "/api/overlay/overrides/")
	if plugin == "" || strings.Contains(plugin, "/") {
		http.Error(w, "plugin name required", http.StatusBadRequest)
		return
	}
	if !datastore.ValidName(plugin) {
		http.Error(w, "invalid plugin name", http.StatusBadRequest)
		return
	}
	if !s.knownPlugin(plugin) {
		http.Error(w, "unknown plugin", http.StatusNotFound)
		return
	}

	switch r.Method {
	case http.MethodPut:
		s.handleOverridePut(w, r, plugin)
	case http.MethodDelete:
		if err := s.deps.Overrides.Delete(plugin); err != nil {
			httpError(w, "delete override", err, http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleOverridePut(w http.ResponseWriter, r *http.Request, plugin string) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 4096))
	if err != nil {
		httpError(w, "read body", err, http.StatusInternalServerError)
		return
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	var partial overrides.Override
	if err := dec.Decode(&partial); err != nil {
		http.Error(w, "invalid body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := partial.Validate(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	merged, err := s.deps.Overrides.MergeOne(plugin, partial)
	if err != nil {
		httpError(w, "save override", err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, merged)
}

func (s *Server) knownPlugin(name string) bool {
	for _, p := range s.deps.Plugins.List() {
		if p.Name == name {
			return true
		}
	}
	return false
}

// overridesChangedEnvelope is the SSE payload published whenever the
// overrides map changes. The shape mirrors the GET endpoint so
// subscribers can reuse merge code unchanged.
type overridesChangedEnvelope struct {
	Event string                       `json:"Event"`
	Data  map[string]overrides.Override `json:"Data"`
}

// MarshalOverridesChanged builds the JSON envelope for an
// _OverridesChanged event. Returns nil bytes on marshal failure (which
// the caller treats as "no event published") rather than panicking.
// Exported so main can publish without owning the envelope shape.
func MarshalOverridesChanged(data map[string]overrides.Override) []byte {
	b, err := json.Marshal(overridesChangedEnvelope{
		Event: "_OverridesChanged",
		Data:  data,
	})
	if err != nil {
		log.Printf("[overrides] marshal _OverridesChanged: %v", err)
		return nil
	}
	return b
}

// handleBootID returns the process-lifetime boot ID. Plugins read this
// on load to detect a backend restart (i.e. a fresh launcher session)
// and reset their per-session state.
func (s *Server) handleBootID(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"bootId":"` + bootid.Get() + `"}`))
}

// handleIdentity exposes the persisted "who am I" identity. GET returns
// the stored Identity (or null), PUT sets it and broadcasts
// _IdentityChanged via the store's Notify hook, DELETE clears it.
func (s *Server) handleIdentity(w http.ResponseWriter, r *http.Request) {
	if s.deps.Identity == nil {
		http.Error(w, "identity store unavailable", http.StatusInternalServerError)
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, s.deps.Identity.Get())
	case http.MethodPut:
		body, err := io.ReadAll(io.LimitReader(r.Body, 4096))
		if err != nil {
			httpError(w, "read body", err, http.StatusInternalServerError)
			return
		}
		var id identity.Identity
		if err := json.Unmarshal(body, &id); err != nil {
			http.Error(w, "invalid body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if err := s.deps.Identity.Set(id); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case http.MethodDelete:
		if err := s.deps.Identity.Clear(); err != nil {
			httpError(w, "clear identity", err, http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
	}
}
