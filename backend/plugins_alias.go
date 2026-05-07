package backend

import "rl-toolkit/backend/internal/plugins"

// PluginManager + companion types are aliased over internal/plugins so
// existing main.go (NewPluginManager) and server.go (s.plugins type)
// stay unchanged. PluginManifest and OverlayConfig are exposed too —
// no current external caller names them, but the JSON shapes are
// public API and worth keeping addressable from this package.
type PluginManager = plugins.Manager
type PluginManifest = plugins.Manifest
type OverlayConfig = plugins.OverlayConfig

func NewPluginManager(dir string) *PluginManager {
	return plugins.New(dir)
}
