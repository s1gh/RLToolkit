//! Global shortcut registration.
//!
//! Uses tauri-plugin-global-shortcut, which on Linux wraps the
//! global-hotkey crate's XGrabKey backend. On Wayland sessions this
//! goes through XWayland, which is present on every mainstream
//! compositor (Hyprland, KDE, GNOME, sway) so the shortcut still
//! delivers system-wide. On X11 sessions the path is the same.
//!
//! The combo is user-configurable via launcher settings
//! (`edit_mode_shortcut`). The default is Ctrl+Shift+E. On any failure
//! we log WARN and continue — the tray menu item and `--toggle-edit`
//! CLI flag remain available as fallbacks.

use std::str::FromStr;
use tauri::{AppHandle, Manager, Runtime};
use tauri_plugin_global_shortcut::{
    GlobalShortcutExt, Shortcut, ShortcutEvent, ShortcutState,
};

pub const DEFAULT_SHORTCUT: &str = "Ctrl+Shift+KeyE";

/// Parse a combo string. Empty / None falls back to the default.
/// Returns the parsed shortcut and the canonical string that was used,
/// so callers can display what is actually in effect.
pub fn parse_or_default(combo: Option<&str>) -> (Shortcut, String) {
    let candidate = combo
        .map(str::trim)
        .filter(|s| !s.is_empty())
        .unwrap_or(DEFAULT_SHORTCUT);
    match Shortcut::from_str(candidate) {
        Ok(s) => (s, candidate.to_string()),
        Err(_) => (
            Shortcut::from_str(DEFAULT_SHORTCUT)
                .expect("DEFAULT_SHORTCUT must parse"),
            DEFAULT_SHORTCUT.to_string(),
        ),
    }
}

fn handler<R: Runtime>(
    app: &AppHandle<R>,
    _sc: &Shortcut,
    event: ShortcutEvent,
) {
    if event.state == ShortcutState::Pressed {
        if let Err(e) = crate::launcher::edit_mode::toggle(app) {
            crate::log_warn!("[overlay-edit] toggle failed: {e}");
        }
    }
}

/// Read the saved combo from settings and register it. Called once at
/// startup.
pub fn register<R: Runtime>(app: &AppHandle<R>) {
    let saved = app
        .try_state::<crate::launcher::ipc::LauncherState>()
        .and_then(|state| state.lock().ok().map(|ctx| ctx.settings.load().edit_mode_shortcut))
        .flatten();
    let (shortcut, label) = parse_or_default(saved.as_deref());
    register_inner(app, shortcut, &label);
}

/// Unregister whatever is currently bound and bind `combo` in its
/// place. Returns the canonical string that was registered, or an
/// error describing the parse / OS failure.
pub fn replace<R: Runtime>(
    app: &AppHandle<R>,
    combo: &str,
) -> Result<String, String> {
    let trimmed = combo.trim();
    if trimmed.is_empty() {
        return Err("shortcut cannot be empty".to_string());
    }
    let parsed = Shortcut::from_str(trimmed)
        .map_err(|e| format!("invalid shortcut '{trimmed}': {e}"))?;

    let gs = app.global_shortcut();
    let _ = gs.unregister_all();

    let combo_owned = trimmed.to_string();
    gs.on_shortcut(parsed, handler::<R>)
        .map_err(|e| format!("register '{trimmed}': {e}"))?;
    crate::log_info!("[launcher] global shortcut {combo_owned} registered");
    Ok(combo_owned)
}

fn register_inner<R: Runtime>(
    app: &AppHandle<R>,
    shortcut: Shortcut,
    label: &str,
) {
    let result = app
        .global_shortcut()
        .on_shortcut(shortcut, handler::<R>);

    match result {
        Ok(()) => crate::log_info!(
            "[launcher] global shortcut {label} registered"
        ),
        Err(e) => crate::log_warn!(
            "[launcher] global shortcut {label} unavailable: {e} \
             (use the tray menu or run `rl-widget --toggle-edit`)"
        ),
    }
}
