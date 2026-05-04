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
    pub tray_ok: bool,
}

pub type LauncherState = Mutex<LauncherCtx>;

#[derive(Serialize)]
#[serde(rename_all = "snake_case")]
pub enum BodyState {
    Dashboard,
    Starting,
    NotResponding,
    Crashed,
    PortConflict,
}

#[derive(Serialize)]
pub struct StatusView {
    pub connected: bool,
    pub starting: bool,
    pub attached: bool,
    pub overlay_enabled: bool,
    pub body_state: BodyState,
    pub message: Option<String>,
    pub tray_ok: bool,
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

    let (body_state, message) = match (&outcome, ctx.starting, ctx.attached) {
        (ProbeOutcome::Toolkit, _, _) => (BodyState::Dashboard, None),
        (_, true, _) => (BodyState::Starting, Some("Starting backend…".to_string())),
        (ProbeOutcome::Unrelated, _, _) => (
            BodyState::PortConflict,
            Some("Port 8080 is already in use by another application.".to_string()),
        ),
        (ProbeOutcome::Unreachable, _, true) => (
            BodyState::NotResponding,
            Some("Backend not responding.".to_string()),
        ),
        (ProbeOutcome::Unreachable, _, false) => (
            BodyState::Crashed,
            Some("Backend crashed — Restart.".to_string()),
        ),
    };

    StatusView {
        connected: matches!(outcome, ProbeOutcome::Toolkit),
        starting: ctx.starting,
        attached: ctx.attached,
        overlay_enabled: ctx.overlay_enabled,
        body_state,
        message,
        tray_ok: ctx.tray_ok,
    }
}

#[tauri::command]
pub fn toggle_overlay(
    enabled: bool,
    app: AppHandle,
    state: State<LauncherState>,
) -> Result<(), String> {
    use tauri::Manager;
    {
        let mut ctx = state.lock().unwrap();
        ctx.overlay_enabled = enabled;
        let mut s = ctx.settings.load();
        s.overlay_enabled = enabled;
        ctx.settings.save(&s).map_err(|e| e.to_string())?;
    } // release the lock before calling out

    if enabled {
        if let Some(w) = app.get_webview_window("main") {
            let _ = w.show();
        } else {
            crate::overlay_bridge::ensure_overlay(&app)?;
        }
    } else if let Some(w) = app.get_webview_window("main") {
        let _ = w.hide();
    }
    Ok(())
}

#[tauri::command]
pub fn restart_backend(app: AppHandle, state: State<LauncherState>) -> Result<(), String> {
    use crate::launcher::backend::spawn_sidecar;
    use tauri::Manager;

    let attached = state.lock().unwrap().attached;
    if attached {
        return Err("backend not owned by launcher".into());
    }

    if let Some(handle) = app.try_state::<crate::launcher::BackendHandle>() {
        let mut slot = handle.0.lock().unwrap();
        if let Some(mut owned) = slot.take() {
            owned.terminate(std::time::Duration::from_secs(2));
        }
        let log_path = std::env::current_dir()
            .unwrap_or_else(|_| std::path::PathBuf::from("."))
            .join("data")
            .join("launcher.log");
        match spawn_sidecar(&app, log_path) {
            Ok(new_owned) => *slot = Some(new_owned),
            Err(e) => return Err(e),
        }
    }
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
