package main

// Version is the rl-toolkit build version. Override at link time:
//
//	go build -ldflags="-X main.Version=0.3.0" ./backend/cmd/rl-toolkit
//
// The plugin catalog filters entries whose min_launcher_version exceeds
// this string, so an unset (or pre-release) build sees the full catalog.
var Version = "0.0.0-dev"

// PluginCatalogURL is the on-network location of plugins.json. Held as
// a build-time var so a corrupted user setting can't redirect updates
// to a malicious host.
var PluginCatalogURL = "https://github.com/s1gh/RLToolkit/releases/download/plugins-latest/plugins.json"
