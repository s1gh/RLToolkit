package main

import (
	"context"
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// web/ contains the dashboard + composite overlay HTML, served straight
// from the binary so we don't depend on the cwd at runtime.
//
//go:embed web/dashboard.html web/overlay.html
var webFS embed.FS

// ─── Configuration ──────────────────────────────────────────

type Config struct {
	RLAddr    string `json:"rl_addr"`
	HTTPPort  int    `json:"http_port"`
	PluginDir string `json:"plugin_dir"`
	DataDir   string `json:"data_dir"`
}

// ─── Event Bus ──────────────────────────────────────────────

// EventBus fans out raw RL messages to all SSE subscribers.
//
// Critical guarantee: Publish never blocks. If a subscriber can't keep up,
// they're immediately evicted (channel closed, removed from bus). If we
// blocked, the upstream readLoop would back up, RL's TCP send buffer would
// fill, and on Linux/Proton RL's main game thread freezes inside send().
// So drops over hangs is the right tradeoff every time.
type subscriber struct {
	ch     chan []byte    // delivered to the SSE handler
	closed chan struct{}  // signals the subscriber has disconnected
}

type EventBus struct {
	subs map[*subscriber]struct{}
	mu   sync.RWMutex
}

func NewEventBus() *EventBus {
	return &EventBus{subs: make(map[*subscriber]struct{})}
}

// Subscribe returns a receive-only channel and a cancel func. The cancel
// func is safe to call multiple times. The channel is closed when canceled
// or when the bus drops the slow subscriber, so receivers must use the
// `msg, ok := <-ch` form.
func (b *EventBus) Subscribe() (<-chan []byte, func()) {
	s := &subscriber{
		// 64-slot buffer ≈ 1 second of 60Hz UpdateStates. Big enough to
		// absorb GC pauses / browser repaint hitches; small enough that a
		// truly stalled consumer is evicted within ~1s instead of taking
		// 4s+ to notice.
		ch:     make(chan []byte, 64),
		closed: make(chan struct{}),
	}
	b.mu.Lock()
	b.subs[s] = struct{}{}
	b.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			b.mu.Lock()
			if _, ok := b.subs[s]; ok {
				delete(b.subs, s)
				close(s.closed)
				close(s.ch)
			}
			b.mu.Unlock()
		})
	}
	return s.ch, cancel
}

// Publish delivers data to every subscriber. Slow subscribers (whose buffer
// is full) are *dropped from the bus* — keeping a stuck SSE client around
// would block the read loop and eventually freeze the upstream connection,
// which we've seen freeze RL itself on Linux/Proton.
func (b *EventBus) Publish(data []byte) {
	// Snapshot so we don't hold the lock while sending.
	b.mu.RLock()
	dst := make([]*subscriber, 0, len(b.subs))
	for s := range b.subs {
		dst = append(dst, s)
	}
	b.mu.RUnlock()

	var slow []*subscriber
	for _, s := range dst {
		select {
		case <-s.closed:
			// already canceled, skip
		case s.ch <- data:
			// delivered
		default:
			// channel full → consumer can't keep up; mark for eviction
			slow = append(slow, s)
		}
	}
	if len(slow) == 0 {
		return
	}
	b.mu.Lock()
	for _, s := range slow {
		if _, ok := b.subs[s]; ok {
			delete(b.subs, s)
			close(s.closed)
			close(s.ch)
		}
	}
	b.mu.Unlock()
	log.Printf("[bus] dropped %d slow subscriber(s)", len(slow))
}

// ─── Data Store (per-plugin JSON file persistence) ──────────

type DataStore struct {
	dir string
	mu  sync.Mutex
}

func NewDataStore(dir string) *DataStore {
	os.MkdirAll(dir, 0755)
	return &DataStore{dir: dir}
}

func (s *DataStore) safePath(plugin string) string {
	clean := strings.NewReplacer("..", "", "/", "", "\\", "").Replace(plugin)
	return filepath.Join(s.dir, clean+".json")
}

func (s *DataStore) LoadAll(plugin string) (map[string]json.RawMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked(plugin)
}

func (s *DataStore) loadLocked(plugin string) (map[string]json.RawMessage, error) {
	data, err := os.ReadFile(s.safePath(plugin))
	if err != nil {
		if os.IsNotExist(err) {
			return make(map[string]json.RawMessage), nil
		}
		return nil, err
	}
	var store map[string]json.RawMessage
	if err := json.Unmarshal(data, &store); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *DataStore) Get(plugin, key string) (json.RawMessage, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	store, err := s.loadLocked(plugin)
	if err != nil {
		return nil, false, err
	}
	val, ok := store[key]
	return val, ok, nil
}

func (s *DataStore) Set(plugin, key string, value json.RawMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	store, _ := s.loadLocked(plugin)
	if store == nil {
		store = make(map[string]json.RawMessage)
	}
	store[key] = value
	data, err := json.Marshal(store)
	if err != nil {
		return err
	}
	return os.WriteFile(s.safePath(plugin), data, 0644)
}

func (s *DataStore) Delete(plugin, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	store, _ := s.loadLocked(plugin)
	if store == nil {
		return nil
	}
	delete(store, key)
	data, err := json.Marshal(store)
	if err != nil {
		return err
	}
	return os.WriteFile(s.safePath(plugin), data, 0644)
}

// ─── Plugin Manager ─────────────────────────────────────────

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

type PluginManager struct {
	dir string

	// Tracks which plugin names we've already logged "Loaded" for, so a
	// re-scan on every List() call doesn't spam the log. Hash of (name,
	// version) so version bumps still surface.
	mu     sync.Mutex
	loaded map[string]string
}

func NewPluginManager(dir string) *PluginManager {
	pm := &PluginManager{dir: dir, loaded: make(map[string]string)}
	pm.List() // initial scan, just to log what's present at startup
	return pm
}

// List reads the plugin directory and returns a fresh manifest list every
// call. This means newly-dropped plugin folders appear without a server
// restart — refreshing the dashboard is enough.
func (pm *PluginManager) List() []*PluginManifest {
	entries, err := os.ReadDir(pm.dir)
	if err != nil {
		log.Printf("[plugins] Cannot read plugin dir %q: %v", pm.dir, err)
		return nil
	}

	pm.mu.Lock()
	defer pm.mu.Unlock()
	seen := make(map[string]struct{}, len(entries))

	out := make([]*PluginManifest, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(pm.dir, e.Name(), "manifest.json"))
		if err != nil {
			continue
		}
		var m PluginManifest
		if err := json.Unmarshal(data, &m); err != nil {
			log.Printf("[plugins] Bad manifest in %s: %v", e.Name(), err)
			continue
		}
		if m.Overlay.Opacity == 0 {
			m.Overlay.Opacity = 1.0
		}
		out = append(out, &m)
		seen[m.Name] = struct{}{}

		// Log on first sight of a plugin (or when its version changes).
		key := m.Name + "@" + m.Version
		if pm.loaded[m.Name] != key {
			log.Printf("[plugins] Loaded: %s v%s (%s)", m.Title, m.Version, m.Name)
			pm.loaded[m.Name] = key
		}
	}

	// Detect removed plugins (folder deleted while server runs) and log once.
	for name := range pm.loaded {
		if _, still := seen[name]; !still {
			log.Printf("[plugins] Removed: %s", name)
			delete(pm.loaded, name)
		}
	}
	return out
}

// ─── RL Stats API Client ────────────────────────────────────

type RLClient struct {
	addr   string
	bus    *EventBus
	status string
	mu     sync.RWMutex

	// outbox decouples the TCP read loop from the bus. The read loop drops
	// every decoded packet onto this channel and immediately returns to
	// reading. A separate dispatcher goroutine drains it into the bus.
	//
	// If the channel is full (very busy bus or slow downstream consumer),
	// the read loop *drops the packet* rather than blocking — keeping the
	// kernel TCP buffer drained is more important than delivering every
	// frame, because a stalled read is what causes RL itself to freeze
	// (its exporter blocks on send() into our full receive buffer).
	outbox chan []byte
}

func NewRLClient(addr string, bus *EventBus) *RLClient {
	return &RLClient{
		addr:   addr,
		bus:    bus,
		status: "disconnected",
		// 1024 ≈ 17 seconds of 60Hz traffic — far more than any GC pause
		// or downstream hiccup we'd ever expect.
		outbox: make(chan []byte, 1024),
	}
}

func (c *RLClient) Status() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.status
}

// dispatcher drains the outbox into the bus. Runs as a single goroutine
// so the publish path is naturally serialized and the read loop never has
// to wait on bus internals.
func (c *RLClient) dispatcher(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-c.outbox:
			if !ok {
				return
			}
			c.bus.Publish(msg)
		}
	}
}

// enqueue is the only path from RL into the bus. Non-blocking by design.
func (c *RLClient) enqueue(msg []byte) {
	select {
	case c.outbox <- msg:
	default:
		// Full — drop the packet. We log occasionally so we know it's
		// happening but don't spam the log at 60Hz.
		dropLogOnce()
	}
}

var (
	lastDropLog time.Time
	dropMu      sync.Mutex
)

func dropLogOnce() {
	dropMu.Lock()
	defer dropMu.Unlock()
	if time.Since(lastDropLog) < 5*time.Second {
		return
	}
	lastDropLog = time.Now()
	log.Printf("[rl-api] outbox full — dropping packets (downstream is too slow)")
}

func (c *RLClient) setStatus(s string) {
	c.mu.Lock()
	c.status = s
	c.mu.Unlock()
	msg, _ := json.Marshal(map[string]string{"Event": "_ConnectionStatus", "Status": s})
	// Status changes are rare and important — block briefly if needed,
	// but never longer than 100ms, so a wedged dispatcher can't pin the
	// reconnect path either.
	select {
	case c.outbox <- msg:
	case <-time.After(100 * time.Millisecond):
	}
}

func (c *RLClient) Run(ctx context.Context) {
	// Single dispatcher drains the outbox into the bus. Lives for the
	// whole client lifetime — no need to restart it across reconnects.
	go c.dispatcher(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		c.setStatus("connecting")
		log.Printf("[rl-api] Connecting to %s …", c.addr)

		dialer := net.Dialer{Timeout: 5 * time.Second}
		conn, err := dialer.DialContext(ctx, "tcp", c.addr)
		if err != nil {
			c.setStatus("disconnected")
			log.Printf("[rl-api] Failed: %v  (retry in 5 s)", err)
			select {
			case <-time.After(5 * time.Second):
			case <-ctx.Done():
				return
			}
			continue
		}

		// Bigger receive buffer = more headroom for the kernel to absorb
		// bursts before backpressuring RL's send(), which is what causes
		// RL to freeze on Linux/Proton.
		if tcp, ok := conn.(*net.TCPConn); ok {
			_ = tcp.SetReadBuffer(1 << 20) // 1 MiB
			_ = tcp.SetNoDelay(true)
		}

		c.setStatus("connected")
		log.Printf("[rl-api] Connected!")
		c.readLoop(ctx, conn)
		conn.Close()
		c.setStatus("disconnected")
		log.Printf("[rl-api] Disconnected — reconnecting in 500 ms …")

		select {
		case <-time.After(500 * time.Millisecond):
		case <-ctx.Done():
			return
		}
	}
}

func (c *RLClient) readLoop(ctx context.Context, conn net.Conn) {
	dec := json.NewDecoder(conn)

	// Silence on this connection for longer than readTimeout means we force
	// a reconnect. RL's exporter sometimes silently stops emitting to an
	// existing connection (especially after a match ends + long menu idle)
	// while leaving the TCP socket alive — but it always emits to a fresh
	// client. So treat any prolonged silence as "connection is stale,
	// reconnect" rather than waiting forever for data that won't come.
	const readTimeout = 30 * time.Second

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		conn.SetReadDeadline(time.Now().Add(readTimeout))

		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			if err == io.EOF {
				return
			}
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				log.Printf("[rl-api] silent for %s — reconnecting", readTimeout)
				return
			}
			log.Printf("[rl-api] Read error: %v", err)
			return
		}

		c.enqueue(raw)
	}
}

// ─── HTTP Server ────────────────────────────────────────────

type Server struct {
	bus     *EventBus
	store   *DataStore
	plugins *PluginManager
	client  *RLClient
	config  Config
	ctx     context.Context
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/events", s.handleSSE)          // SSE stream
	mux.HandleFunc("/api/data/", s.handleData)       // Plugin persistence
	mux.HandleFunc("/api/plugins", s.handlePlugins)  // Plugin listing
	mux.HandleFunc("/api/status", s.handleStatus)    // Connection status
	mux.HandleFunc("/overlay", s.handleOverlay)      // Overlay compositor
	mux.HandleFunc("/sdk.js", s.handleSDKJS)         // Plugin SDK
	mux.HandleFunc("/sdk.css", s.handleSDKCSS)       // Shared design tokens
	mux.HandleFunc("/favicon.ico", s.handleFavicon)  // Inline favicon
	mux.Handle("/plugins/", http.StripPrefix("/plugins/",
		http.FileServer(http.Dir(s.config.PluginDir))))
	mux.HandleFunc("/", s.handleDashboard)

	return cors(mux)
}

func cors(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET,POST,PUT,DELETE,OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == "OPTIONS" {
			w.WriteHeader(204)
			return
		}
		h.ServeHTTP(w, r)
	})
}

// ── SSE ─────────────────────────────────────────────────────

func (s *Server) handleSSE(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch, cancel := s.bus.Subscribe()
	defer cancel()

	// Per-write deadline: if the client TCP buffer is stalled (hidden tab,
	// frozen OBS browser source, dead network), an Fprintf can block
	// indefinitely. We force a 2s write deadline so a dead client surfaces
	// as a write error → handler returns → cancel() runs → slot freed.
	rc := http.NewResponseController(w)

	writeAndFlush := func(format string, args ...interface{}) bool {
		if err := rc.SetWriteDeadline(time.Now().Add(2 * time.Second)); err != nil {
			// Some response writers don't support deadlines (e.g. behind
			// reverse proxies). Fall back to a best-effort write.
		}
		if _, err := fmt.Fprintf(w, format, args...); err != nil {
			return false
		}
		if err := rc.Flush(); err != nil {
			return false
		}
		return true
	}

	// Initial status push.
	init, _ := json.Marshal(map[string]string{"Event": "_ConnectionStatus", "Status": s.client.Status()})
	if !writeAndFlush("data: %s\n\n", init) {
		return
	}

	// Heartbeat every 15s — keeps proxies / OBS browser source from idling
	// the connection out, and surfaces dead clients quickly.
	hb := time.NewTicker(15 * time.Second)
	defer hb.Stop()

	for {
		select {
		case msg, ok := <-ch:
			if !ok {
				return // bus dropped us (slow consumer) or canceled
			}
			if !writeAndFlush("data: %s\n\n", msg) {
				return
			}
		case <-hb.C:
			if !writeAndFlush(": ping\n\n") {
				return
			}
		case <-r.Context().Done():
			return
		case <-s.ctx.Done():
			return
		}
	}
}

// ── Data REST API ───────────────────────────────────────────

func (s *Server) handleData(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/data/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) == 0 || parts[0] == "" {
		http.Error(w, "plugin name required", 400)
		return
	}
	plugin := parts[0]
	key := ""
	if len(parts) > 1 {
		key = parts[1]
	}

	w.Header().Set("Content-Type", "application/json")

	switch r.Method {
	case "GET":
		if key == "" {
			all, err := s.store.LoadAll(plugin)
			if err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			json.NewEncoder(w).Encode(all)
		} else {
			val, ok, err := s.store.Get(plugin, key)
			if err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			if !ok {
				http.Error(w, "not found", 404)
				return
			}
			w.Write(val)
		}

	case "POST", "PUT":
		if key == "" {
			http.Error(w, "key required", 400)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 10<<20))
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		if err := s.store.Set(plugin, key, json.RawMessage(body)); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.WriteHeader(204)

	case "DELETE":
		if key == "" {
			http.Error(w, "key required", 400)
			return
		}
		if err := s.store.Delete(plugin, key); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.WriteHeader(204)
	}
}

func (s *Server) handlePlugins(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s.plugins.List())
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"rl_api": s.client.Status()})
}

// ── Overlay Compositor ──────────────────────────────────────

func (s *Server) handleOverlay(w http.ResponseWriter, r *http.Request) {
	serveEmbeddedHTML(w, "web/overlay.html")
}

func serveEmbeddedHTML(w http.ResponseWriter, path string) {
	data, err := webFS.ReadFile(path)
	if err != nil {
		http.Error(w, "asset missing: "+path, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(data)
}

func (s *Server) handleSDKJS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write([]byte(sdkJS))
}

func (s *Server) handleSDKCSS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write([]byte(sdkCSS))
}

// faviconSVG mirrors the dashboard's logo gradient so the browser tab and
// the dashboard's "RL" tile feel like the same brand.
const faviconSVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 32 32">
  <defs>
    <linearGradient id="g" x1="0" y1="0" x2="32" y2="32" gradientUnits="userSpaceOnUse">
      <stop offset="0" stop-color="#22d3ee"/>
      <stop offset="1" stop-color="#a78bfa"/>
    </linearGradient>
  </defs>
  <rect width="32" height="32" rx="8" fill="url(#g)"/>
  <text x="50%" y="55%" text-anchor="middle" dominant-baseline="middle"
        font-family="system-ui,sans-serif" font-weight="800" font-size="14"
        fill="#0a0c14" letter-spacing="-0.5">RL</text>
</svg>`

func (s *Server) handleFavicon(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Write([]byte(faviconSVG))
}

// ── Dashboard ───────────────────────────────────────────────

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	serveEmbeddedHTML(w, "web/dashboard.html")
}

// ─── Main ───────────────────────────────────────────────────

func main() {
	rlAddr := flag.String("rl-addr", "127.0.0.1:49123", "RL Stats API address (host:port)")
	httpPort := flag.Int("port", 8080, "HTTP server port")
	pluginDir := flag.String("plugins", "plugins", "Plugin directory path")
	dataDir := flag.String("data", "data", "Data directory path")
	flag.Parse()

	cfg := Config{
		RLAddr:    *rlAddr,
		HTTPPort:  *httpPort,
		PluginDir: *pluginDir,
		DataDir:   *dataDir,
	}

	log.SetFlags(log.Ltime)

	bus := NewEventBus()
	store := NewDataStore(cfg.DataDir)
	pm := NewPluginManager(cfg.PluginDir)
	client := NewRLClient(cfg.RLAddr, bus)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	srv := &Server{bus: bus, store: store, plugins: pm, client: client, config: cfg, ctx: ctx}

	go client.Run(ctx)

	hs := &http.Server{Addr: fmt.Sprintf(":%d", cfg.HTTPPort), Handler: srv.routes()}

	go func() {
		log.Printf("[server] Dashboard → http://localhost:%d", cfg.HTTPPort)
		log.Printf("[server] Overlay   → http://localhost:%d/overlay", cfg.HTTPPort)
		log.Printf("[server] Waiting for Rocket League Stats API on %s …", cfg.RLAddr)
		if err := hs.ListenAndServe(); err != http.ErrServerClosed {
			log.Fatalf("[server] %v", err)
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	log.Println("[server] Shutting down …")
	cancel()
	shutCtx, shutCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer shutCancel()
	hs.Shutdown(shutCtx)
}
