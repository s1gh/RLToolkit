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
