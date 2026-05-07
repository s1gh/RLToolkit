// Package paths resolves the canonical OS-standard locations for
// everything the toolkit persists. Mirrors the Rust side's
// crate::paths in overlay/src-tauri so the launcher, the bundled
// backend, and the `rl-toolkit dev` CLI all agree on where state
// lives.
//
// Base directory:
//
//	Linux:   $XDG_DATA_HOME/RLToolkit  (or ~/.local/share/RLToolkit)
//	macOS:   ~/Library/Application Support/RLToolkit
//	Windows: %LOCALAPPDATA%\RLToolkit
//
// Note: Go's stdlib has os.UserConfigDir, os.UserCacheDir, and
// os.UserHomeDir but no equivalent of dirs::data_local_dir(). On
// Windows os.UserConfigDir returns %APPDATA% (roaming), not
// %LOCALAPPDATA%, and on Linux it returns $XDG_CONFIG_HOME, not
// $XDG_DATA_HOME. So we resolve the data-local equivalent by hand to
// stay aligned with Tauri's choice.
package paths

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
)

// AppDir returns the per-OS application data directory all toolkit
// state lives under.
func AppDir() (string, error) {
	base, err := dataLocalDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "RLToolkit"), nil
}

// DataDir returns <AppDir>/data.
func DataDir() (string, error) {
	app, err := AppDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(app, "data"), nil
}

// PluginsDir returns <AppDir>/plugins.
func PluginsDir() (string, error) {
	app, err := AppDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(app, "plugins"), nil
}

// dataLocalDir mirrors Rust's dirs::data_local_dir():
//
//	Linux:   $XDG_DATA_HOME or $HOME/.local/share
//	macOS:   $HOME/Library/Application Support
//	Windows: %LOCALAPPDATA%
func dataLocalDir() (string, error) {
	switch runtime.GOOS {
	case "windows":
		if v := os.Getenv("LOCALAPPDATA"); v != "" {
			return v, nil
		}
		// Fallback for stripped environments — rare in practice but
		// don't crash on missing env vars.
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, "AppData", "Local"), nil
	case "darwin":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, "Library", "Application Support"), nil
	case "linux":
		if v := os.Getenv("XDG_DATA_HOME"); v != "" {
			return v, nil
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".local", "share"), nil
	default:
		return "", errors.New("unsupported platform")
	}
}
