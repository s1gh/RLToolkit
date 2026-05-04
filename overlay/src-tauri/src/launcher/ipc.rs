//! Tauri commands invoked from launcher.js.

use crate::launcher::backend::{probe_status, ProbeOutcome};
use crate::launcher::settings::SettingsStore;
use serde::Serialize;
use std::sync::Mutex;
use tauri::{AppHandle, State};

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
pub fn get_toolkit_url(state: State<LauncherState>) -> String {
    state.lock().unwrap().toolkit_url.clone()
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
    use tauri_plugin_opener::OpenerExt;
    let path = std::env::current_dir()
        .map(|p| p.join("data"))
        .map_err(|e| e.to_string())?;
    app.opener()
        .open_path(path.to_string_lossy().to_string(), None::<&str>)
        .map_err(|e| e.to_string())
}

#[tauri::command]
pub fn open_dashboard_in_browser(app: AppHandle, state: State<LauncherState>) -> Result<(), String> {
    use tauri_plugin_opener::OpenerExt;
    let url = state.lock().unwrap().toolkit_url.clone();
    app.opener().open_url(url, None::<&str>).map_err(|e| e.to_string())
}

#[tauri::command]
pub fn quit(app: AppHandle) {
    app.exit(0);
}
