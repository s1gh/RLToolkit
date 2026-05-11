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
	"net/url"
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
	"rl-toolkit/backend/internal/plugincatalog"
	"rl-toolkit/backend/internal/plugins"
	"rl-toolkit/backend/internal/replaywatch"
	"rl-toolkit/backend/internal/roster"
	"rl-toolkit/backend/internal/source"
	"rl-toolkit/backend/internal/state"
	"rl-toolkit/backend/internal/surface"
	"rl-toolkit/backend/internal/types"
	"strings"
	"sync"
	"time"
)

// Tunables. A stalled SSE write eventually fills its bus slot; on
// Linux/Proton that backpressure can reach RL's send() and freeze
// the game thread. Disconnect stuck clients rather than block.
const (
	sseHeartbeat     = 15 * time.Second
	sseWriteDeadline = 2 * time.Second

	// MaxPluginValueBytes caps a single Set body to keep a misbehaving
	// plugin from filling memory or disk.
	MaxPluginValueBytes = 10 << 20 // 10 MiB
)

// Deps bundles every long-lived dependency the server reads from.
type Deps struct {
	Bus           *bus.Bus
	Store         *datastore.Store
	Plugins       *plugins.Manager
	Source        *source.RL
	MatchState    *state.MatchState
	Roster        *roster.Tracker
	Demos         *emit.Demos
	Overrides     *overrides.Store
	Surface       *surface.Store
	Discoveries   *discoveries.Store
	Identity      *identity.Store
	Catalog       *plugincatalog.Manager
	ReplayWatcher *replaywatch.Watcher
	PluginDir     string
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
	mux.HandleFunc("/api/plugins/updates", s.handlePluginUpdates)
	mux.HandleFunc("/api/plugins/refresh-catalog", s.handleRefreshCatalog)
	mux.HandleFunc("/api/plugins/install-update", s.handleInstallUpdate)
	mux.HandleFunc("/api/plugins/", s.handlePluginByName)
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
	mux.HandleFunc("/api/overlay/surface", s.handleOverlaySurface)
	mux.HandleFunc("/api/overlay/surface/detected", s.handleOverlaySurfaceDetected)
	mux.HandleFunc("/api/replay-watcher", s.handleReplayWatcher)
	mux.HandleFunc("/api/replay-file", s.handleReplayFile)
	mux.HandleFunc("/api/plugin-fetch/", s.handlePluginFetch)
	mux.HandleFunc("/overlay", s.handleOverlay)
	mux.HandleFunc("/sdk.js", s.handleSDKJS)
	mux.HandleFunc("/sdk.css", s.handleSDKCSS)
	mux.HandleFunc("/fonts/", s.handleFont)
	mux.HandleFunc("/overlay-editor.js", s.handleOverlayEditorJS)
	mux.HandleFunc("/favicon.ico", s.handleFavicon)
	// Plugin assets serve straight from disk so iteration is
	// edit-then-refresh. requireManifest gates on a valid
	// manifest.json (broken plugins 404 instead of half-loading);
	// noCache forces revalidation so edits show up on next request.
	mux.Handle("/plugins/", http.StripPrefix("/plugins/",
		s.requireManifest(noCache(http.HandlerFunc(s.servePluginAsset)))))
	mux.HandleFunc("/", s.handleDashboard)

	return cors(mux)
}

// noCache wraps a handler with `Cache-Control: no-cache` so responses
// always revalidate (304 on unchanged content; not "don't cache").
func noCache(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		h.ServeHTTP(w, r)
	})
}

// requireManifest blocks plugin asset requests when the manifest is
// missing or malformed, so a broken plugin can't half-load.
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
// when one is registered for <name>, otherwise from PluginDir.
// http.FileServer doesn't fit because the root is per-request.
func (s *Server) servePluginAsset(w http.ResponseWriter, r *http.Request) {
	rel := strings.TrimPrefix(r.URL.Path, "/")
	name, rest, _ := strings.Cut(rel, "/")
	if name == "" {
		http.NotFound(w, r)
		return
	}

	root := s.deps.PluginDir
	if devPath := s.deps.Plugins.DevPath(name); devPath != "" {
		s.serveFromRoot(w, r, devPath, rest)
		return
	}
	s.serveFromRoot(w, r, filepath.Join(root, name), rest)
}

// handleSideload accepts a multipart upload of a .rltp under the form
// field "file" and installs it. The 64 MiB cap fails runaway uploads
// fast — generous given today's largest plugin is well under 100 KiB.
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

// handlePluginByName operates on a single installed plugin keyed by
// its directory name. Currently only DELETE is handled (uninstall);
// other methods 405. Refuses dev-shadowed plugins because removing
// the on-disk copy while a `rl-toolkit dev` registration is active
// would silently leave the dev version running and confuse the user.
func (s *Server) handlePluginByName(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/api/plugins/")
	if name == "" || strings.Contains(name, "/") {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodDelete {
		w.Header().Set("Allow", "DELETE")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.deps.Plugins != nil {
		if devPath := s.deps.Plugins.DevPath(name); devPath != "" {
			http.Error(w, "plugin is registered for dev hot-reload; stop `rl-toolkit dev` first", http.StatusConflict)
			return
		}
		if !s.deps.Plugins.Has(name) {
			http.NotFound(w, r)
			return
		}
	}
	if err := install.Uninstall(name, s.deps.PluginDir); err != nil {
		http.Error(w, "uninstall: "+err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
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

// cors permits any origin: the server is loopback-bound in practice
// and reached from arbitrary local pages (OBS overlays, dashboard,
// plugin dev servers).
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
// into a set. Empty input → nil (all events delivered). Synthetic
// framing events bypass the filter on the bus side.
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
	// Replay the cached roster so a plugin connecting mid-match sees
	// the current player list immediately.
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

// broadcastStoreChanged emits _StoreChanged so live SDK contexts
// (overlay, dashboard preview, settings) can react when a key in
// their namespace changes elsewhere. Broadcast is unfiltered; the
// SDK filters by namespace in RLT.store.onChange.
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

// handleFont serves bundled woff2 fonts from the embed FS. The
// /fonts/<name>.woff2 suffix check prevents traversal into other
// embedded assets. Long Cache-Control because filenames are
// content-addressed; a swap lands via rename and busts the cache.
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
	Event string                        `json:"Event"`
	Data  map[string]overrides.Override `json:"Data"`
}

// MarshalOverridesChanged builds the JSON envelope for an
// _OverridesChanged event. Returns nil on marshal failure.
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

// ── Overlay surface API ─────────────────────────────────────

// handleOverlaySurface routes GET (read configured + detected + effective)
// and PUT (set or clear configured) on /api/overlay/surface. PUT body
// is either {"width": int, "height": int} or the literal `null` to clear.
func (s *Server) handleOverlaySurface(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, surfaceResponse{
			Configured: s.deps.Surface.Get(),
			Detected:   s.deps.Surface.Detected(),
			Effective:  s.deps.Surface.Effective(),
		})
	case http.MethodPut:
		s.handleSurfacePut(w, r)
	default:
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
	}
}

type surfaceResponse struct {
	Configured *surface.Size `json:"configured"`
	Detected   *surface.Size `json:"detected"`
	Effective  surface.Size  `json:"effective"`
}

func (s *Server) handleSurfacePut(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1024))
	if err != nil {
		httpError(w, "read body", err, http.StatusInternalServerError)
		return
	}
	// `null` clears the value: json.Unmarshal of null into **Size
	// leaves the pointer nil, which is what Set wants.
	var sz *surface.Size
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&sz); err != nil {
		http.Error(w, "invalid body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.deps.Surface.Set(sz); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, surfaceResponse{
		Configured: s.deps.Surface.Get(),
		Detected:   s.deps.Surface.Detected(),
		Effective:  s.deps.Surface.Effective(),
	})
}

// handleOverlaySurfaceDetected accepts a POST from rl-widget reporting
// its bound monitor's logical size. In-memory only; resets on backend
// restart. Returns 204 on success, 400 on invalid body.
func (s *Server) handleOverlaySurfaceDetected(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1024))
	if err != nil {
		httpError(w, "read body", err, http.StatusInternalServerError)
		return
	}
	var sz surface.Size
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&sz); err != nil {
		http.Error(w, "invalid body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.deps.Surface.SetDetected(&sz); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// surfaceChangedEnvelope is the SSE payload published when the configured
// surface changes. Mirrors overridesChangedEnvelope shape.
type surfaceChangedEnvelope struct {
	Event string          `json:"Event"`
	Data  surfaceResponse `json:"Data"`
}

// MarshalSurfaceChanged builds the JSON envelope for a _SurfaceChanged
// event. Returns nil on marshal failure.
func MarshalSurfaceChanged(configured, detected *surface.Size, effective surface.Size) []byte {
	b, err := json.Marshal(surfaceChangedEnvelope{
		Event: "_SurfaceChanged",
		Data: surfaceResponse{
			Configured: configured,
			Detected:   detected,
			Effective:  effective,
		},
	})
	if err != nil {
		log.Printf("[surface] marshal _SurfaceChanged: %v", err)
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

// ── Replay watcher API ──────────────────────────────────────

// handleReplayWatcher routes GET (current state) and PUT (set or
// clear configured dir) on /api/replay-watcher.
func (s *Server) handleReplayWatcher(w http.ResponseWriter, r *http.Request) {
	if s.deps.ReplayWatcher == nil {
		http.Error(w, "replay watcher unavailable", http.StatusInternalServerError)
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, s.deps.ReplayWatcher.State())
	case http.MethodPut:
		s.handleReplayWatcherPut(w, r)
	default:
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
	}
}

// replayWatcherPutBody mirrors surface's PUT shape: {"dir":"..."} sets,
// {"dir":null} (or empty body) clears.
type replayWatcherPutBody struct {
	Dir *string `json:"dir"`
}

func (s *Server) handleReplayWatcherPut(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 4096))
	if err != nil {
		httpError(w, "read body", err, http.StatusInternalServerError)
		return
	}
	var b replayWatcherPutBody
	if len(body) > 0 {
		dec := json.NewDecoder(bytes.NewReader(body))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&b); err != nil {
			http.Error(w, "invalid body: "+err.Error(), http.StatusBadRequest)
			return
		}
	}
	dir := ""
	if b.Dir != nil {
		dir = *b.Dir
	}
	if err := s.deps.ReplayWatcher.Set(dir); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, s.deps.ReplayWatcher.State())
}

// replayWatcherChangedEnvelope is the SSE payload published when the
// replay-watcher state changes. Mirrors surfaceChangedEnvelope.
type replayWatcherChangedEnvelope struct {
	Event string            `json:"Event"`
	Data  replaywatch.State `json:"Data"`
}

// MarshalReplayWatcherChanged builds the JSON envelope for a
// _ReplayWatcherChanged event. Returns nil on marshal failure.
func MarshalReplayWatcherChanged(state replaywatch.State) []byte {
	b, err := json.Marshal(replayWatcherChangedEnvelope{
		Event: "_ReplayWatcherChanged",
		Data:  state,
	})
	if err != nil {
		log.Printf("[replaywatch] marshal _ReplayWatcherChanged: %v", err)
		return nil
	}
	return b
}

// replayFileMaxBytes caps the size of a file the toolkit will serve
// over /api/replay-file. The largest replays observed on disk are
// well under 2 MB; the cap is generous headroom that still flags
// "something is wrong, don't read this".
//
// var (not const) so tests can substitute a smaller value.
var replayFileMaxBytes int64 = 10 << 20 // 10 MiB

// handleReplayFile streams a .replay file's bytes from inside the
// watcher's effective directory. Used by the bundled
// ballchasing-upload plugin (and any future plugin that needs to
// read replay bytes from JS).
//
// Validation:
//   - 503 if the watcher has no effective directory.
//   - 403 if the resolved path escapes the effective directory.
//   - 403 if the file's extension is not .replay (case-insensitive).
//   - 404 if the file does not exist.
//   - 413 if the file's size exceeds replayFileMaxBytes.
func (s *Server) handleReplayFile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}
	if s.deps.ReplayWatcher == nil {
		http.Error(w, "replay watcher unavailable", http.StatusInternalServerError)
		return
	}
	effective := s.deps.ReplayWatcher.State().Effective
	if effective == "" {
		http.Error(w, "replay watcher inactive", http.StatusServiceUnavailable)
		return
	}

	rawPath := r.URL.Query().Get("path")
	if rawPath == "" {
		http.Error(w, "missing path parameter", http.StatusBadRequest)
		return
	}

	// Reject by extension first, before any filesystem work.
	if !strings.EqualFold(filepath.Ext(rawPath), ".replay") {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	resolvedPath, err := filepath.EvalSymlinks(filepath.Clean(rawPath))
	if err != nil {
		if os.IsNotExist(err) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	resolvedRoot, err := filepath.EvalSymlinks(filepath.Clean(effective))
	if err != nil {
		http.Error(w, "replay watcher inactive", http.StatusServiceUnavailable)
		return
	}

	rel, err := filepath.Rel(resolvedRoot, resolvedPath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	info, err := os.Stat(resolvedPath)
	if err != nil {
		if os.IsNotExist(err) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		http.Error(w, "stat: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if info.Size() > replayFileMaxBytes {
		http.Error(w, "file too large", http.StatusRequestEntityTooLarge)
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeFile(w, r, resolvedPath)
}

// ── Plugin fetch proxy ──────────────────────────────────────

// pluginFetchClient is the outbound HTTP client used by the plugin
// fetch proxy. Redirects are NOT followed: the plugin sees the 3xx
// response and decides what to do, which prevents an attacker who
// controls an allowlisted upstream from redirecting to a private
// network address (SSRF). Timeout is generous because uploads can be
// large; per-request cancellation comes from the request context.
var pluginFetchClient = &http.Client{
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
	Timeout: 60 * time.Second,
}

// pluginFetchMaxBytes caps both the request body the plugin sends
// through the proxy AND the response body the proxy returns. var
// (not const) so tests can substitute a smaller value.
var pluginFetchMaxBytes int64 = 16 << 20 // 16 MiB

// handlePluginFetch proxies a plugin's outbound request to an
// allowlisted external origin. Plugins running in the toolkit's iframe
// can't talk directly to APIs that don't return CORS headers (most
// don't), so the plugin POSTs to /api/plugin-fetch/<name>?url=<target>
// and the server forwards the request.
//
// Security model:
//   - Plugin must declare the target's origin in its manifest's
//     permissions.connect array.
//   - Target URL must be https and parse to a bare origin matching
//     one of the allowlisted entries (no path/port deviation tolerated
//     beyond what sanitizeConnectPermissions already accepted).
//   - Redirects are not followed (returned to the plugin verbatim) to
//     prevent SSRF via 3xx into private networks.
//   - Sensitive headers from the inbound request (Cookie, Host) are
//     stripped; only Authorization, Content-Type, Accept, and
//     Retry-After are copied through. The plugin can still send any
//     body it wants — the bytes are forwarded as-is.
func (s *Server) handlePluginFetch(w http.ResponseWriter, r *http.Request) {
	if s.deps.Plugins == nil {
		http.Error(w, "plugin manager unavailable", http.StatusInternalServerError)
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/api/plugin-fetch/")
	if name == "" || strings.Contains(name, "/") {
		http.NotFound(w, r)
		return
	}
	manifest := s.deps.Plugins.Get(name)
	if manifest == nil {
		http.NotFound(w, r)
		return
	}
	if manifest.Permissions == nil || len(manifest.Permissions.Connect) == 0 {
		http.Error(w, "plugin has no connect permissions", http.StatusForbidden)
		return
	}

	rawURL := r.URL.Query().Get("url")
	if rawURL == "" {
		http.Error(w, "missing url parameter", http.StatusBadRequest)
		return
	}
	target, err := url.Parse(rawURL)
	if err != nil {
		http.Error(w, "invalid url: "+err.Error(), http.StatusBadRequest)
		return
	}
	if target.Scheme != "https" {
		http.Error(w, "only https targets are allowed", http.StatusForbidden)
		return
	}
	if !originAllowed(target, manifest.Permissions.Connect) {
		http.Error(w, "target origin not in plugin's connect allowlist", http.StatusForbidden)
		return
	}

	// Clone the body up to the cap. http.MaxBytesReader closes r.Body
	// for us when the cap trips.
	var body io.Reader
	if r.Body != nil {
		body = http.MaxBytesReader(w, r.Body, pluginFetchMaxBytes)
	}

	upstream, err := http.NewRequestWithContext(r.Context(), r.Method, target.String(), body)
	if err != nil {
		http.Error(w, "build upstream request: "+err.Error(), http.StatusInternalServerError)
		return
	}
	for _, h := range []string{"Authorization", "Content-Type", "Accept"} {
		if v := r.Header.Get(h); v != "" {
			upstream.Header.Set(h, v)
		}
	}

	resp, err := pluginFetchClient.Do(upstream)
	if err != nil {
		http.Error(w, "upstream: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	for _, h := range []string{"Content-Type", "Retry-After"} {
		if v := resp.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	// Cap response body too so a malicious upstream can't fill memory.
	_, _ = io.CopyN(w, resp.Body, pluginFetchMaxBytes)
}

// originAllowed reports whether the target's origin (scheme://host[:port])
// matches any entry in the allowlist. Both sides have already been
// validated as bare https origins, so a string compare on the
// canonicalized form is sufficient.
func originAllowed(target *url.URL, allow []string) bool {
	wantHost := target.Host
	for _, entry := range allow {
		u, err := url.Parse(entry)
		if err != nil {
			continue
		}
		if u.Scheme == target.Scheme && u.Host == wantHost {
			return true
		}
	}
	return false
}
