//! Bridge from the launcher (lib crate) to the overlay setup code (bin crate).
//! Exposed via a function pointer set by main.rs at startup.

use std::sync::OnceLock;
use tauri::AppHandle;

pub type OverlayFactory = fn(&AppHandle) -> Result<(), String>;

static FACTORY: OnceLock<OverlayFactory> = OnceLock::new();

pub fn install(f: OverlayFactory) {
    let _ = FACTORY.set(f);
}

pub fn ensure_overlay(app: &AppHandle) -> Result<(), String> {
    match FACTORY.get() {
        Some(f) => f(app),
        None => Err("overlay factory not installed".to_string()),
    }
}
