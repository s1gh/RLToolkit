//! Launcher mode for rl-widget.

pub mod backend;
pub mod settings;

#[cfg(test)]
mod tests;

use crate::cli::Args;
use tauri::Builder;

/// Apply launcher-mode plugins to the Tauri builder. Called from `main.rs`
/// only when launcher mode is selected. Today we register:
/// - `tauri-plugin-shell` (sidecar process spawning)
/// - `tauri-plugin-single-instance` (focus existing window on second launch)
pub fn run(_args: Args) {
    eprintln!("[rl-widget] launcher mode placeholder — pass --no-launcher to use the overlay");
    std::process::exit(0);
}

pub fn install_plugins<R: tauri::Runtime>(builder: Builder<R>) -> Builder<R> {
    builder
        .plugin(tauri_plugin_shell::init())
        .plugin(tauri_plugin_single_instance::init(|app, _argv, _cwd| {
            use tauri::Manager;
            if let Some(w) = app.get_webview_window("launcher") {
                let _ = w.unminimize();
                let _ = w.show();
                let _ = w.set_focus();
            }
        }))
}
