//! Linux dispatcher. Picks X11 or Wayland once at first call based on
//! environment, then sticks with the choice for the lifetime of the
//! process.

use crate::focus_watcher::ForegroundInfo;
use std::sync::OnceLock;

mod linux_wayland;
mod linux_x11;

#[derive(Clone, Copy)]
enum Backend {
    X11,
    Wayland,
}

static BACKEND: OnceLock<Backend> = OnceLock::new();

fn pick_backend() -> Backend {
    // Native Wayland when WAYLAND_DISPLAY is set and XDG_SESSION_TYPE
    // isn't "x11"; otherwise X11 (which also reaches XWayland clients
    // via _NET_ACTIVE_WINDOW).
    let wl = std::env::var_os("WAYLAND_DISPLAY").is_some();
    let session = std::env::var("XDG_SESSION_TYPE").unwrap_or_default();
    let x11_session = session.eq_ignore_ascii_case("x11");
    if wl && !x11_session {
        Backend::Wayland
    } else {
        Backend::X11
    }
}

pub fn query_foreground() -> Option<ForegroundInfo> {
    let backend = *BACKEND.get_or_init(pick_backend);
    match backend {
        Backend::X11 => linux_x11::query_foreground(),
        Backend::Wayland => linux_wayland::query_foreground(),
    }
}

/// Block until the next focus-relevant event or `timeout`. Wayland
/// waits on the compositor socket fd, so Alt-Tab is observed within
/// ~1ms instead of up to the run loop's prior 100ms tick. X11 has no
/// equivalent wired up yet — falls back to a capped sleep so it keeps
/// the old 10 Hz polling cadence (the run loop hands us a bigger
/// idle-ceiling timeout that's only appropriate for event-driven
/// backends).
pub fn wait_for_event(timeout: std::time::Duration) {
    let backend = *BACKEND.get_or_init(pick_backend);
    match backend {
        Backend::Wayland => linux_wayland::wait_for_event(timeout),
        Backend::X11 => {
            let capped = timeout.min(crate::focus_watcher::POLL_INTERVAL);
            std::thread::sleep(capped);
        }
    }
}
