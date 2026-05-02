//! Per-OS foreground-window query. cfg-dispatching wrapper.

use super::ForegroundInfo;

#[cfg(target_os = "windows")]
mod windows;
#[cfg(target_os = "linux")]
mod linux;

#[cfg(target_os = "windows")]
pub fn query_foreground() -> Option<ForegroundInfo> {
    windows::query_foreground()
}

#[cfg(target_os = "linux")]
pub fn query_foreground() -> Option<ForegroundInfo> {
    linux::query_foreground()
}

#[cfg(not(any(target_os = "windows", target_os = "linux")))]
pub fn query_foreground() -> Option<ForegroundInfo> {
    None
}
