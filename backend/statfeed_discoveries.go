package backend

import "rl-toolkit/backend/internal/discoveries"

// StatfeedDiscoveryStore aliases internal/discoveries.Store so existing
// callers (main.go, server.go) keep compiling.
type StatfeedDiscoveryStore = discoveries.Store
type StatfeedDiscovery = discoveries.Entry

func NewStatfeedDiscoveryStore(dataDir string) *StatfeedDiscoveryStore {
	return discoveries.New(dataDir)
}
