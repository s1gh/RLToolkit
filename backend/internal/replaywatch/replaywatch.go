// Package replaywatch watches Rocket League's Demos directory for newly
// saved replay files and broadcasts a _SavedReplay event when one
// finishes being written. RL doesn't surface this through the Stats
// API, so we observe the filesystem instead.
//
// The package has three pieces:
//
//   - Store (store.go) — JSON-backed user override for the replay
//     directory, mirroring the surface/overrides pattern.
//   - ResolveDir (this file) — pure path resolver: configured override
//     or per-OS auto-detection.
//   - Watcher (watcher.go) — fsnotify loop + size-stability debouncer
//     that publishes _SavedReplay to the bus.
package replaywatch

import (
	"path/filepath"
	"time"
)

// SteamAppID is RL's Steam appid; the Wine prefix lives at
// compatdata/<id>/pfx on Linux Steam installs.
const SteamAppID = "252950"

// DefaultStableDelay is how long the file size must be unchanged before
// the watcher fires _SavedReplay. RL writes the .replay file in chunks;
// this delay separates "still writing" from "done writing".
const DefaultStableDelay = 1500 * time.Millisecond

// ResolveDir picks the effective Demos directory.
//
// configured: user override (the value persisted in Store). Empty means
// "use auto-detection".
//
// goos: runtime.GOOS at the call site (injected for testability).
//
// env / home / existsFn: environment + filesystem accessors, also
// injected so tests don't have to set real env vars or create real dirs.
//
// Returns (path, source) where source is one of:
//
//   - "configured" — user override (returned verbatim, no existence check)
//   - "auto"       — first existing per-OS candidate
//   - "none"       — nothing matched (path is "")
func ResolveDir(
	configured string,
	goos string,
	env func(string) string,
	home string,
	existsFn func(string) bool,
) (string, string) {
	if configured != "" {
		return filepath.Clean(configured), "configured"
	}
	for _, c := range autoCandidates(goos, env, home) {
		if existsFn(c) {
			return c, "auto"
		}
	}
	return "", "none"
}

// autoCandidates returns the per-OS list of paths to probe, in priority
// order. macOS returns nil — RL doesn't run natively, so users with a
// Wine/CrossOver prefix must set a custom dir.
func autoCandidates(goos string, env func(string) string, home string) []string {
	switch goos {
	case "windows":
		userprofile := env("USERPROFILE")
		if userprofile == "" {
			return nil
		}
		// Construct with backslashes manually rather than filepath.Join,
		// because filepath.Join uses the host OS separator. The function
		// takes goos as a parameter precisely so it can answer for any
		// target OS, including from tests running on a non-Windows host.
		return []string{
			userprofile + `\Documents\My Games\Rocket League\TAGame\Demos`,
		}
	case "linux":
		var roots []string
		if scdp := env("STEAM_COMPAT_DATA_PATH"); scdp != "" {
			roots = append(roots, scdp)
		}
		if home != "" {
			roots = append(roots,
				filepath.Join(home, ".local", "share", "Steam", "steamapps", "compatdata", SteamAppID),
				filepath.Join(home, ".steam", "steam", "steamapps", "compatdata", SteamAppID),
			)
		}
		var out []string
		for _, root := range roots {
			// Try "Documents" first (modern Wine/Proton), then
			// "My Documents" (older prefixes).
			for _, docs := range []string{"Documents", "My Documents"} {
				out = append(out,
					filepath.Join(root, "pfx", "drive_c", "users", "steamuser", docs, "My Games", "Rocket League", "TAGame", "Demos"),
				)
			}
		}
		return out
	default:
		return nil
	}
}
