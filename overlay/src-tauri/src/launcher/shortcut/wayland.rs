//! Wayland portal backend -- placeholder. Real implementation lands in
//! a follow-up commit. Until then, a Wayland session falls through to
//! a WARN and relies on tray/CLI for activation.

use tauri::{AppHandle, Runtime};

pub fn register_portal<R: Runtime>(_app: &AppHandle<R>) {
    crate::log_warn!(
        "[launcher] global shortcut on Wayland: portal backend not yet implemented \
         (use the tray menu or run `rl-widget --toggle-edit`)"
    );
}
