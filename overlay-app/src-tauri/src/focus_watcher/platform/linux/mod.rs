//! Linux dispatcher. Picks X11 or Wayland once at first call based on
//! environment, then sticks with the choice for the lifetime of the
//! process.

use crate::focus_watcher::ForegroundInfo;
use std::sync::OnceLock;

mod linux_x11;
// linux_wayland is added in Task 8.
// mod linux_wayland;

#[derive(Clone, Copy)]
enum Backend {
    X11,
    // Wayland — added in Task 8.
}

static BACKEND: OnceLock<Backend> = OnceLock::new();

fn pick_backend() -> Backend {
    // For now, every Linux session uses X11. The Wayland path is added in
    // the next task. On a Wayland session this still works because XWayland
    // exposes _NET_ACTIVE_WINDOW for X11 clients (including this binary if
    // Tauri/GTK opens an X11 connection). However, RL itself runs under
    // Proton on Wayland sessions and shows up via XWayland too, so X11
    // detection is the working path. The Wayland-native path in the next
    // task is the fallback for native-Wayland compositors that don't
    // happen to expose XWayland's _NET_ACTIVE_WINDOW.
    Backend::X11
}

pub fn query_foreground() -> Option<ForegroundInfo> {
    let backend = *BACKEND.get_or_init(pick_backend);
    match backend {
        Backend::X11 => linux_x11::query_foreground(),
    }
}
