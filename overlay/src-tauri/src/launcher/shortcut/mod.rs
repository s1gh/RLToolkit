//! Global shortcut registration -- session-aware facade.
//!
//! Two backends:
//! - tauri-plugin-global-shortcut (Windows, Linux X11, macOS). Wraps
//!   the global-hotkey crate; uses RegisterHotKey / XGrabKey.
//! - XDG org.freedesktop.portal.GlobalShortcuts via ashpd
//!   (Linux Wayland). See shortcut/wayland.rs.
//!
//! Detection: read XDG_SESSION_TYPE. "wayland" picks the portal path;
//! anything else picks the plugin path. The plugin path on Linux
//! Wayland tries to register via XWayland and silently fails to
//! receive events when a Wayland-native game holds focus, which is
//! exactly why we need the portal.
//!
//! On any failure we log WARN and continue. The tray menu and
//! `--toggle-edit` CLI flag are always available as fallbacks.

use tauri::{AppHandle, Runtime};

#[cfg(target_os = "linux")]
mod wayland;

pub fn register<R: Runtime>(app: &AppHandle<R>) {
    #[cfg(target_os = "linux")]
    {
        if is_wayland_session() {
            wayland::register_portal(app);
            return;
        }
    }
    register_plugin(app);
}

#[cfg(target_os = "linux")]
fn is_wayland_session() -> bool {
    std::env::var("XDG_SESSION_TYPE")
        .map(|v| v.eq_ignore_ascii_case("wayland"))
        .unwrap_or(false)
}

fn register_plugin<R: Runtime>(app: &AppHandle<R>) {
    use tauri_plugin_global_shortcut::{Code, GlobalShortcutExt, Modifiers, Shortcut, ShortcutEvent, ShortcutState};

    let shortcut = Shortcut::new(
        Some(Modifiers::CONTROL | Modifiers::SHIFT),
        Code::KeyE,
    );

    let result = app.global_shortcut().on_shortcut(
        shortcut,
        |app: &AppHandle<R>, _sc: &Shortcut, event: ShortcutEvent| {
            if event.state == ShortcutState::Pressed {
                crate::launcher::edit_mode::probe_toggle(app);
            }
        },
    );

    match result {
        Ok(()) => crate::log_info!(
            "[launcher] global shortcut Ctrl+Shift+E registered (plugin backend)"
        ),
        Err(e) => crate::log_warn!(
            "[launcher] global shortcut Ctrl+Shift+E unavailable: {e} \
             (use the tray menu or run `rl-widget --toggle-edit`)"
        ),
    }
}
