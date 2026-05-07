package backend

import "time"

// Config is the runtime configuration assembled from CLI flags in main().
type Config struct {
	RLAddr    string `json:"rl_addr"`
	HTTPPort  int    `json:"http_port"`
	PluginDir string `json:"plugin_dir"`
	DataDir   string `json:"data_dir"`
}

// HTTP / SSE tunables. RL TCP-client tunables (outboxBufSize,
// dialTimeout, etc.) live in internal/source. Bus tunables live in
// internal/bus.
const (
	sseHeartbeat     = 15 * time.Second
	sseWriteDeadline = 2 * time.Second
	httpShutdown     = 2 * time.Second

	// maxPluginValueBytes caps a single Set body to keep a misbehaving
	// plugin from filling memory or disk.
	maxPluginValueBytes = 10 << 20 // 10 MiB
)
