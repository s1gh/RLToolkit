//! Per-OS foreground-window query. cfg-dispatching wrapper.

use super::{ForegroundInfo, MatchRule};

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

/// Decide whether the foreground window matches the rule, with
/// platform-specific quirks the generic `query_foreground` path can't
/// express. Returns:
///   - `Some(true)`  — the rule's target is considered foreground.
///   - `Some(false)` — something else is foreground (overlay should hide).
///   - `None`        — no signal this tick; the run loop holds its state.
///
/// On platforms where the foreground answer is reliable, this is just
/// `query_foreground` + `rule.apply` + self-PID filter. Wayland gets a
/// custom impl because Hyprland's wlr-foreign-toplevel manager does
/// not mark XWayland fullscreen toplevels (Rocket League on Proton) as
/// `activated`, so the activated-only view of foreground misses the
/// most important case the watcher exists to detect. The Wayland impl
/// falls back to "rule-matching toplevel exists" when nothing is
/// activated.
pub fn query_match(rule: &MatchRule, self_pid: u32) -> Option<bool> {
    #[cfg(target_os = "linux")]
    {
        return linux::query_match(rule, self_pid);
    }
    #[cfg(not(target_os = "linux"))]
    {
        let info = query_foreground()?;
        if info.pid == self_pid {
            return None;
        }
        Some(rule.apply(&info))
    }
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
