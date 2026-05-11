//! Per-OS foreground-window query. cfg-dispatching wrapper.

use super::ForegroundInfo;

#[cfg(target_os = "windows")]
mod windows;
#[cfg(target_os = "linux")]
mod linux;
#[cfg(target_os = "macos")]
mod macos;

#[cfg(target_os = "windows")]
pub fn query_foreground() -> Option<ForegroundInfo> {
    windows::query_foreground()
}

#[cfg(target_os = "linux")]
pub fn query_foreground() -> Option<ForegroundInfo> {
    linux::query_foreground()
}

#[cfg(target_os = "macos")]
pub fn query_foreground() -> Option<ForegroundInfo> {
    macos::query_foreground()
}

#[cfg(not(any(target_os = "windows", target_os = "linux", target_os = "macos")))]
pub fn query_foreground() -> Option<ForegroundInfo> {
    None
}

/// Block until the next focus-relevant event or `timeout` elapses,
/// whichever comes first. Platforms that have a real event source
/// (currently: Wayland) wait on the OS-side fd so a focus change
/// wakes the watcher within ~1ms. Platforms that only support polling
/// fall back to a plain sleep of `timeout`. The run loop calls this
/// in place of `thread::sleep` so the existing time-based debounce
/// transitions still fire when the OS is silent.
#[cfg(target_os = "linux")]
pub fn wait_for_event(timeout: std::time::Duration) {
    linux::wait_for_event(timeout);
}

#[cfg(not(target_os = "linux"))]
pub fn wait_for_event(timeout: std::time::Duration) {
    // No event-driven backend wired up here — cap at the legacy
    // polling interval so we keep the 10 Hz cadence the run loop
    // used to enforce directly. Without the cap the idle ceiling
    // (1s) would silently slow Windows/macOS focus detection.
    let capped = timeout.min(super::POLL_INTERVAL);
    std::thread::sleep(capped);
}
