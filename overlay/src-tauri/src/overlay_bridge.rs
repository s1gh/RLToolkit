//! Bridge from the launcher (lib crate) to the overlay setup code (bin crate).
//! Exposed via a pair of function pointers set by main.rs at startup —
//! one to (re)build the overlay window, one to navigate the pre-built
//! webview to the live toolkit URL.

use std::sync::OnceLock;
use tauri::AppHandle;

pub type OverlayFactory = fn(&AppHandle) -> Result<(), String>;
pub type OverlayNavigator = fn(&AppHandle, &str) -> Result<(), String>;

static FACTORY: OnceLock<OverlayFactory> = OnceLock::new();
static NAVIGATOR: OnceLock<OverlayNavigator> = OnceLock::new();

pub fn install(f: OverlayFactory) {
    let _ = FACTORY.set(f);
}

pub fn install_navigator(f: OverlayNavigator) {
    let _ = NAVIGATOR.set(f);
}

pub fn ensure_overlay(app: &AppHandle) -> Result<(), String> {
    match FACTORY.get() {
        Some(f) => f(app),
        None => Err("overlay factory not installed".to_string()),
    }
}

/// Navigate the pre-built overlay webview to `<toolkit>/overlay`. The
/// launcher probe thread calls this once /api/status confirms the
/// backend is up, replacing the transparent placeholder the window
/// was built with.
pub fn navigate_overlay(app: &AppHandle, toolkit_url: &str) -> Result<(), String> {
    match NAVIGATOR.get() {
        Some(f) => f(app, toolkit_url),
        None => Err("overlay navigator not installed".to_string()),
    }
}
