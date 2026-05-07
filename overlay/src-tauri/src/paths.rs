//! Canonical filesystem locations for everything the app persists.
//!
//! All user state lives under a single per-OS base directory so the
//! launcher, the bundled backend, and the `rl-toolkit dev` CLI agree on
//! where to find each file. The base is the platform's standard
//! application-data dir + `RLToolkit`:
//!
//!   Linux:   $XDG_DATA_HOME/RLToolkit  (or ~/.local/share/RLToolkit)
//!   macOS:   ~/Library/Application Support/RLToolkit
//!   Windows: %LOCALAPPDATA%\RLToolkit
//!
//! These match `dirs::data_local_dir()` on every platform; the Go side
//! resolves the same path via its own helper (see backend/internal/paths).

use std::path::PathBuf;

/// Base dir for all RL Toolkit state. Falls back to `.` when no OS-
/// standard dir is resolvable (CI containers, weird sandboxes) — better
/// to keep the app working with a relative path than to refuse to start.
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

/// Path to the launcher's sidecar log. Lives under data/ so all
/// generated files share one parent.
pub fn launcher_log_path() -> PathBuf {
    default_data_dir().join("launcher.log")
}
