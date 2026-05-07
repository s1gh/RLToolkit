package backend

import "time"

// Config is the runtime configuration assembled from CLI flags in main().
type Config struct {
	RLAddr    string `json:"rl_addr"`
	HTTPPort  int    `json:"http_port"`
	PluginDir string `json:"plugin_dir"`
	DataDir   string `json:"data_dir"`
}

// httpShutdown is the only main-side tunable left here. SSE / RL TCP
// tunables now live with their owning packages (internal/server,
// internal/source).
const httpShutdown = 2 * time.Second
