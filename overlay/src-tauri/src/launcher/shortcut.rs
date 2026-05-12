//! Global shortcut registration.
//!
//! Uses tauri-plugin-global-shortcut, which on Linux wraps the
//! global-hotkey crate's XGrabKey backend. On Wayland sessions this
//! goes through XWayland, which is present on every mainstream
//! compositor (Hyprland, KDE, GNOME, sway) so the shortcut still
//! delivers system-wide. On X11 sessions the path is the same.
//!
//! On any failure we log WARN and continue. The tray menu item and
//! `--toggle-edit` CLI flag remain available as fallbacks.

use tauri::{AppHandle, Runtime};

pub fn register<R: Runtime>(app: &AppHandle<R>) {
    use tauri_plugin_global_shortcut::{
        Code, GlobalShortcutExt, Modifiers, Shortcut, ShortcutEvent, ShortcutState,
    };

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
            "[launcher] global shortcut Ctrl+Shift+E registered"
        ),
        Err(e) => crate::log_warn!(
            "[launcher] global shortcut Ctrl+Shift+E unavailable: {e} \
             (use the tray menu or run `rl-widget --toggle-edit`)"
        ),
    }
}
