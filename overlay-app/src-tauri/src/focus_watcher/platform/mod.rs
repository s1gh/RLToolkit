//! Per-OS foreground-window query. cfg-dispatching wrapper.

use super::ForegroundInfo;

#[cfg(target_os = "windows")]
mod windows;

#[cfg(target_os = "windows")]
pub fn query_foreground() -> Option<ForegroundInfo> {
    windows::query_foreground()
}

#[cfg(not(target_os = "windows"))]
pub fn query_foreground() -> Option<ForegroundInfo> {
    // Real implementations land in later tasks. Until then, returning
    // None means "no signal" — the watcher leaves state unchanged and
    // the overlay stays in its initial Active state (visible).
    None
}
