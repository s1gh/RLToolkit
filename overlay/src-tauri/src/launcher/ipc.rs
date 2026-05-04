//! Tauri commands invoked from launcher.js.

use crate::launcher::backend::{probe_status, ProbeOutcome};
use crate::launcher::settings::SettingsStore;
use serde::Serialize;
use std::sync::Mutex;
use tauri::{AppHandle, Manager, State};

pub struct LauncherCtx {
    pub toolkit_url: String,
    pub settings: SettingsStore,
    pub overlay_enabled: bool,
    pub attached: bool,
    pub starting: bool,
}

pub type LauncherState = Mutex<LauncherCtx>;

#[derive(Serialize)]
pub struct StatusView {
    pub connected: bool,
    pub starting: bool,
    pub attached: bool,
    pub overlay_enabled: bool,
}

#[tauri::command]
pub fn get_status(state: State<LauncherState>) -> StatusView {
    let ctx = state.lock().unwrap();
    let outcome = probe_status(
        &format!("{}/api/status", ctx.toolkit_url.trim_end_matches('/')),
        std::time::Duration::from_millis(500),
    );
    StatusView {
        connected: matches!(outcome, ProbeOutcome::Toolkit),
        starting: ctx.starting,
        attached: ctx.attached,
        overlay_enabled: ctx.overlay_enabled,
    }
}

#[tauri::command]
pub fn toggle_overlay(enabled: bool, state: State<LauncherState>) -> Result<(), String> {
    let mut ctx = state.lock().unwrap();
    ctx.overlay_enabled = enabled;
    let mut s = ctx.settings.load();
    s.overlay_enabled = enabled;
    ctx.settings.save(&s).map_err(|e| e.to_string())?;
    // Phase C will hook the actual overlay window show/hide.
    Ok(())
}

#[tauri::command]
pub fn restart_backend() -> Result<(), String> {
    // Phase C: implemented when ownership is held in shared state.
    Ok(())
}

#[tauri::command]
pub fn open_data_folder(app: AppHandle) -> Result<(), String> {
    use tauri_plugin_shell::ShellExt;
    let path = std::env::current_dir()
        .map(|p| p.join("data"))
        .map_err(|e| e.to_string())?;
    app.shell()
        .open(path.to_string_lossy().to_string(), None)
        .map_err(|e| e.to_string())
}

#[tauri::command]
pub fn open_dashboard_in_browser(app: AppHandle, state: State<LauncherState>) -> Result<(), String> {
    use tauri_plugin_shell::ShellExt;
    let url = state.lock().unwrap().toolkit_url.clone();
    app.shell().open(url, None).map_err(|e| e.to_string())
}

#[tauri::command]
pub fn quit(app: AppHandle) {
    app.exit(0);
}
