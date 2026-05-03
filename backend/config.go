package main

import "time"

// Config is the runtime configuration assembled from CLI flags in main().
type Config struct {
	RLAddr    string `json:"rl_addr"`
	HTTPPort  int    `json:"http_port"`
	PluginDir string `json:"plugin_dir"`
	DataDir   string `json:"data_dir"`
}

// Tunables for the RL TCP client and SSE fan-out. The numbers here are
// load-bearing: a stalled SSE write or a backed-up bus eventually fills
// RL's TCP send buffer, and on Linux/Proton that freezes RL's main game
// thread inside send(). The bus, the outbox, and the SSE writer all
// prefer "drop / disconnect" over "block" for that reason.
const (
	// subscriberBufSize ≈ 1s of 60Hz UpdateStates per SSE client. Big
	// enough to absorb GC pauses and browser repaint hitches, small
	// enough that a truly stalled consumer is evicted within ~1s.
	subscriberBufSize = 64

	// outboxBufSize ≈ 17s of 60Hz traffic between the TCP read loop and
	// the bus dispatcher. Far more than any expected GC or downstream
	// hiccup; if it ever fills, packets are dropped (not blocked).
	outboxBufSize = 1024

	// tcpReadBuffer increases kernel headroom so bursts don't backpressure
	// RL's send().
	tcpReadBuffer = 1 << 20 // 1 MiB

	dialTimeout       = 5 * time.Second
	reconnectDelay    = 500 * time.Millisecond
	rlIdleTimeout     = 30 * time.Second // force reconnect after silence
	statusPushTimeout = 100 * time.Millisecond
	dropLogInterval   = 5 * time.Second

	// idleLogInterval rate-limits the "silent for 30s — reconnecting"
	// cycle so a long menu / between-matches stretch doesn't fill the
	// terminal with one cycle log every 30s. The first cycle in any
	// quiet stretch always logs (the limiter starts cleared); repeats
	// inside this window stay silent, and a real-traffic / dial-failure
	// event resets it so the next idle cycle is informative again.
	idleLogInterval = 5 * time.Minute

	sseHeartbeat     = 15 * time.Second
	sseWriteDeadline = 2 * time.Second
	httpShutdown     = 2 * time.Second

	// maxPluginValueBytes caps a single Set body to keep a misbehaving
	// plugin from filling memory or disk.
	maxPluginValueBytes = 10 << 20 // 10 MiB
)
