//! Canonical filesystem locations for everything the app persists.
//! Mirrors `dirs::data_local_dir()` + `RLToolkit`; the Go side
//! resolves the same path via backend/internal/paths.
//!
//!   Linux:   $XDG_DATA_HOME/RLToolkit (or ~/.local/share/RLToolkit)
//!   macOS:   ~/Library/Application Support/RLToolkit
//!   Windows: %LOCALAPPDATA%\RLToolkit

use std::path::PathBuf;

/// Base dir for all RL Toolkit state. Falls back to `.` for sandboxes
/// where the OS-standard dir can't be resolved.
pub fn base_dir() -> PathBuf {
    dirs::data_local_dir()
        .unwrap_or_else(|| PathBuf::from("."))
        .join("RLToolkit")
}

/// Path to the launcher settings file (overlay enabled, window
/// geometry, user-set plugin/data/RL-API overrides).
pub fn launcher_settings_path() -> PathBuf {
    base_dir().join("launcher.json")
}

/// Default backend data directory. Holds overrides, identity,
/// encounters, datastore, discoveries. The user can override via the
/// Settings dialog; the override persists in launcher.json.
pub fn default_data_dir() -> PathBuf {
    base_dir().join("data")
}

/// Default plugins directory. The user can override via Settings.
pub fn default_plugins_dir() -> PathBuf {
    base_dir().join("plugins")
}

/// Path to the sidecar's stdout/stderr capture file. Catches output
/// that bypasses the Go backend's structured log (runtime panics, raw
/// `fmt.Print`, stack traces).
pub fn sidecar_log_path(date_stamp: &str) -> PathBuf {
    default_data_dir()
        .join("logs")
        .join(format!("sidecar-{date_stamp}.log"))
}

/// Directory holding all rotated log files (launcher-*, backend-*,
/// sidecar-*). Bug-report hand-off: "zip and send this folder."
pub fn logs_dir() -> PathBuf {
    default_data_dir().join("logs")
}
