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
use tauri_plugin_global_shortcut::{
    Code, GlobalShortcutExt, Modifiers, Shortcut, ShortcutEvent, ShortcutState,
};

fn toggle_shortcut() -> Shortcut {
    Shortcut::new(Some(Modifiers::CONTROL | Modifiers::SHIFT), Code::KeyE)
}

fn esc_shortcut() -> Shortcut {
    Shortcut::new(Some(Modifiers::empty()), Code::Escape)
}

pub fn register<R: Runtime>(app: &AppHandle<R>) {
    let result = app.global_shortcut().on_shortcut(
        toggle_shortcut(),
        |app: &AppHandle<R>, _sc: &Shortcut, event: ShortcutEvent| {
            if event.state == ShortcutState::Pressed {
                if let Err(e) = crate::launcher::edit_mode::toggle(app) {
                    crate::log_warn!("[overlay-edit] toggle failed: {e}");
                }
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

/// Register Esc as a temporary global shortcut while edit mode is
/// active. The overlay window has `set_keyboard_interactivity(false)`
/// on its layer-shell surface, so a `keydown` listener inside the page
/// never fires. A global registration sidesteps that.
///
/// Best-effort: on failure we log and continue. The other exit paths
/// (the same Ctrl+Shift+E toggle, the tray item, the dashboard button,
/// the CLI flag) keep working regardless.
///
/// Runs on a worker thread because the plugin's register call uses
/// `run_on_main_thread`. If we called it directly from inside another
/// shortcut handler (which is itself running on the main thread), we
/// would deadlock waiting for our own main-thread task to drain.
pub fn register_esc<R: Runtime>(app: &AppHandle<R>) {
    let app = app.clone();
    std::thread::spawn(move || {
        let result = app.global_shortcut().on_shortcut(
            esc_shortcut(),
            |app: &AppHandle<R>, _sc: &Shortcut, event: ShortcutEvent| {
                if event.state == ShortcutState::Pressed {
                    if let Err(e) = crate::launcher::edit_mode::set(app, false) {
                        crate::log_warn!("[overlay-edit] esc exit failed: {e}");
                    }
                }
            },
        );
        if let Err(e) = result {
            crate::log_warn!("[overlay-edit] esc shortcut unavailable: {e}");
        }
    });
}

/// Pair to `register_esc`. Same threading concern: runs on a worker
/// because the plugin's unregister also uses `run_on_main_thread`.
pub fn unregister_esc<R: Runtime>(app: &AppHandle<R>) {
    let app = app.clone();
    std::thread::spawn(move || {
        if let Err(e) = app.global_shortcut().unregister(esc_shortcut()) {
            crate::log_warn!("[overlay-edit] esc unregister failed: {e}");
        }
    });
}
