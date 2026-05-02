package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Server wires HTTP handlers to the EventBus, DataStore, PluginManager,
// and RLClient. It owns no goroutines of its own; long-lived per-request
// goroutines (notably handleSSE) listen on r.Context().
type Server struct {
	bus       *EventBus
	store     *DataStore
	plugins   *PluginManager
	client    *RLClient
	lifecycle *LifecycleTracker
	overrides *OverridesStore
	config    Config
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/events", s.handleSSE)
	mux.HandleFunc("/api/data/", s.handleData)
	mux.HandleFunc("/api/plugins", s.handlePlugins)
	mux.HandleFunc("/api/events", s.handleEventCatalog)
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/lifecycle", s.handleLifecycle)
	mux.HandleFunc("/api/metrics", s.handleMetrics)
	mux.HandleFunc("/api/overlay/overrides", s.handleOverlayOverridesAll)
	mux.HandleFunc("/api/overlay/overrides/", s.handleOverlayOverridesOne)
	mux.HandleFunc("/overlay", s.handleOverlay)
	mux.HandleFunc("/sdk.js", s.handleSDKJS)
	mux.HandleFunc("/sdk.css", s.handleSDKCSS)
	mux.HandleFunc("/overlay-editor.js", s.handleOverlayEditorJS)
	mux.HandleFunc("/favicon.ico", s.handleFavicon)
	mux.Handle("/plugins/", http.StripPrefix("/plugins/",
		http.FileServer(http.Dir(s.config.PluginDir))))
	mux.HandleFunc("/", s.handleDashboard)

	return cors(mux)
}

// cors permits any origin: the server is meant to be reached from
// arbitrary local pages (overlays in OBS, dashboard, plugin dev servers)
// and is loopback-bound in practice.
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

// writeAsset writes a precomputed byte slice with the given content type
// and (optional) cache control header.
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

// SSE wire-format byte slices reused per write.
var (
	sseDataPrefix = []byte("data: ")
	sseRecordEnd  = []byte("\n\n")
	ssePingFrame  = []byte(": ping\n\n")
	sseFramePool  = sync.Pool{New: func() any { return bytes.NewBuffer(make([]byte, 0, 512)) }}
)

// parseEventsFilter turns a comma-separated `?events=A,B,C` query value
// into a set the bus can check per-publish. Empty input → nil (no
// filter, all events delivered). Whitespace and empty entries are
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

	ch, cancel := s.bus.Subscribe(parseEventsFilter(r.URL.Query().Get("events")))
	defer cancel()

	// Per-write deadline: an Fprintf can block indefinitely against a
	// stalled client TCP buffer (hidden tab, frozen OBS browser source,
	// dead network). We force a deadline so a dead client surfaces as a
	// write error → handler returns → cancel() runs → bus slot freed.
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

	if !writeFrame(sseDataPrefix, newStatusEvent(s.client.Status()), sseRecordEnd) {
		return
	}
	// Initial lifecycle snapshot so the SDK doesn't have to wait for the
	// next state change to know what's going on. Same convention as
	// _ConnectionStatus — push current state on connect.
	if s.lifecycle != nil {
		snap := s.lifecycle.Snapshot()
		if init, err := json.Marshal(lifecycleEvent{
			Event:       "_Lifecycle",
			MatchActive: snap.MatchActive,
			Phase:       snap.Phase,
			MatchGUID:   snap.MatchGUID,
			Since:       snap.Since,
		}); err == nil {
			if !writeFrame(sseDataPrefix, init, sseRecordEnd) {
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
				return // bus dropped us (slow consumer) or canceled
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
		all, err := s.store.LoadAll(plugin)
		if err != nil {
			httpError(w, "load all", err, http.StatusInternalServerError)
			return
		}
		writeJSON(w, all)
		return
	}
	val, ok, err := s.store.Get(plugin, key)
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
	body, err := io.ReadAll(io.LimitReader(r.Body, maxPluginValueBytes))
	if err != nil {
		httpError(w, "read body", err, http.StatusInternalServerError)
		return
	}
	if err := s.store.Set(plugin, key, json.RawMessage(body)); err != nil {
		httpError(w, "set", err, http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDataDelete(w http.ResponseWriter, plugin, key string) {
	if key == "" {
		http.Error(w, "key required", http.StatusBadRequest)
		return
	}
	if err := s.store.Delete(plugin, key); err != nil {
		httpError(w, "delete", err, http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handlePlugins(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, s.plugins.List())
}

func (s *Server) handleEventCatalog(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, EventCatalog)
}

func (s *Server) handleStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]RLStatus{"rl_api": s.client.Status()})
}

// handleLifecycle returns the current authoritative gameplay snapshot
// (match_active, phase, match_guid, since). Useful for poll-based
// consumers that don't subscribe to SSE — dashboards, eww/ags widgets,
// the desktop widget supervisor, etc.
func (s *Server) handleLifecycle(w http.ResponseWriter, _ *http.Request) {
	if s.lifecycle == nil {
		writeJSON(w, LifecycleSnapshot{Phase: PhaseNone, Since: time.Now()})
		return
	}
	writeJSON(w, s.lifecycle.Snapshot())
}

// handleMetrics exposes the event-bus telemetry: subscriber count,
// publish/delivery counters, eviction count, throughput, and publish
// latency percentiles over a recent window. Cheap to call (single
// snapshot read + small sort); fine for /api/metrics-style polling.
func (s *Server) handleMetrics(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, s.bus.Metrics())
}

// ── Static / embedded assets ────────────────────────────────

func (s *Server) handleOverlay(w http.ResponseWriter, _ *http.Request) {
	writeAsset(w, "text/html; charset=utf-8", "", overlayHTML)
}

func (s *Server) handleSDKJS(w http.ResponseWriter, _ *http.Request) {
	writeAsset(w, "application/javascript; charset=utf-8", "no-cache", sdkJSBytes)
}

func (s *Server) handleSDKCSS(w http.ResponseWriter, _ *http.Request) {
	writeAsset(w, "text/css; charset=utf-8", "no-cache", sdkCSSBytes)
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
	writeAsset(w, "text/html; charset=utf-8", "", dashboardHTML)
}

// ── Overlay overrides API ───────────────────────────────────

// handleOverlayOverridesAll serves the full overrides map. Used by both
// the editor (to seed UI state) and the production /overlay page (to
// merge per-plugin overrides over manifest defaults).
func (s *Server) handleOverlayOverridesAll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, s.overrides.GetAll())
}

// handleOverlayOverridesOne handles PUT (merge a partial override onto an
// existing entry) and DELETE (reset that plugin to manifest defaults).
// The plugin name comes from the URL tail and must match a known plugin
// — we don't allow writes for nonexistent plugins, otherwise stale
// entries pile up forever.
func (s *Server) handleOverlayOverridesOne(w http.ResponseWriter, r *http.Request) {
	plugin := strings.TrimPrefix(r.URL.Path, "/api/overlay/overrides/")
	if plugin == "" || strings.Contains(plugin, "/") {
		http.Error(w, "plugin name required", http.StatusBadRequest)
		return
	}
	if !pluginNamePattern.MatchString(plugin) {
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
		if err := s.overrides.Delete(plugin); err != nil {
			httpError(w, "delete override", err, http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
	}
}

// handleOverridePut decodes the body strictly (rejects unknown fields so
// the on-disk schema stays clean), validates it via the store, and
// returns the merged entry.
func (s *Server) handleOverridePut(w http.ResponseWriter, r *http.Request, plugin string) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 4096))
	if err != nil {
		httpError(w, "read body", err, http.StatusInternalServerError)
		return
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	var partial OverlayOverride
	if err := dec.Decode(&partial); err != nil {
		http.Error(w, "invalid body: "+err.Error(), http.StatusBadRequest)
		return
	}
	// Pre-validate so a request-side error becomes 400 with the
	// validation message. Anything that fails inside MergeOne after
	// this point is either a corrupt-on-disk merged value or a persist
	// failure — both deserve 500 + a generic message via httpError so
	// filesystem paths and disk errors don't leak to the client.
	if err := partial.validate(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	merged, err := s.overrides.MergeOne(plugin, partial)
	if err != nil {
		httpError(w, "save override", err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, merged)
}

// knownPlugin returns true if `name` is in the current plugin catalog.
// The catalog re-scans on its own, so this reflects the live set —
// disabling/removing a plugin folder makes its overrides PUTs return 404.
func (s *Server) knownPlugin(name string) bool {
	for _, p := range s.plugins.List() {
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
	Event string                     `json:"Event"`
	Data  map[string]OverlayOverride `json:"Data"`
}

// marshalOverridesChanged builds the JSON envelope for an
// _OverridesChanged event. Returns nil bytes on marshal failure (logged
// and treated as "no event published") rather than panicking.
func marshalOverridesChanged(data map[string]OverlayOverride) []byte {
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
