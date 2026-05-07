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
    /// True when the user explicitly stopped the backend via Stop. Distinct
    /// from "crashed" so the UI shows "Start backend" instead of "Restart".
    pub stopped_by_user: bool,
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
    Stopped,
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
    pub stopped_by_user: bool,
    pub rl_api: Option<String>,
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

    let (body_state, message) = match (&outcome, ctx.starting, ctx.attached, ctx.stopped_by_user) {
        (ProbeOutcome::Toolkit { .. }, _, _, _) => (BodyState::Dashboard, None),
        (_, true, _, _) => (BodyState::Starting, Some("Starting backend…".to_string())),
        (ProbeOutcome::Unrelated, _, _, _) => (
            BodyState::PortConflict,
            Some("Port is already in use by another application.".to_string()),
        ),
        (ProbeOutcome::Unreachable, _, _, true) => (
            BodyState::Stopped,
            Some("Backend stopped.".to_string()),
        ),
        (ProbeOutcome::Unreachable, _, true, _) => (
            BodyState::NotResponding,
            Some("Backend not responding.".to_string()),
        ),
        (ProbeOutcome::Unreachable, _, false, _) => (
            BodyState::Crashed,
            Some("Backend crashed — Restart.".to_string()),
        ),
    };

    let rl_api = match &outcome {
        ProbeOutcome::Toolkit { rl_api } => Some(rl_api.clone()),
        _ => None,
    };

    StatusView {
        connected: matches!(outcome, ProbeOutcome::Toolkit { .. }),
        starting: ctx.starting,
        attached: ctx.attached,
        overlay_enabled: ctx.overlay_enabled,
        body_state,
        message,
        tray_ok: ctx.tray_ok,
        stopped_by_user: ctx.stopped_by_user,
        rl_api,
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

    // The overlay window was created during launcher setup() and lives
    // for the app's lifetime — never built from this IPC handler. Building
    // a webview window from an IPC worker thread deadlocks WebView2 on
    // Windows; show/hide is safe from any thread.
    if let Some(w) = app.get_webview_window("main") {
        if enabled {
            let _ = w.show();
        } else {
            let _ = w.hide();
        }
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

    let (plugins_dir, data_dir, rl_addr) = {
        let ctx = state.lock().unwrap();
        let s = ctx.settings.load();
        (s.plugins_dir.clone(), s.data_dir.clone(), s.rl_addr.clone())
    };

    if let Some(handle) = app.try_state::<crate::launcher::BackendHandle>() {
        let mut slot = handle.0.lock().unwrap();
        if let Some(mut owned) = slot.take() {
            owned.terminate(std::time::Duration::from_secs(2));
        }
        let log_path = crate::paths::launcher_log_path();
        match spawn_sidecar(&app, log_path, plugins_dir, data_dir, rl_addr) {
            Ok(new_owned) => {
                *slot = Some(new_owned);
                drop(slot);
                state.lock().unwrap().stopped_by_user = false;
            }
            Err(e) => return Err(e),
        }
    }
    Ok(())
}

/// Stop the running backend. Errors when attached (we don't own it).
#[tauri::command]
pub fn stop_backend(app: AppHandle, state: State<LauncherState>) -> Result<(), String> {
    use crate::launcher::backend::BackendOwnership;
    use tauri::Manager;

    if state.lock().unwrap().attached {
        return Err("backend not owned by launcher".into());
    }

    if let Some(handle) = app.try_state::<crate::launcher::BackendHandle>() {
        let mut slot = handle.0.lock().unwrap();
        if let Some(mut owned) = slot.take() {
            owned.terminate(std::time::Duration::from_secs(2));
        }
        *slot = Some(BackendOwnership::StoppedByUser);
        drop(slot);
        state.lock().unwrap().stopped_by_user = true;
    }
    Ok(())
}

/// Start the backend. Used after stop_backend; spawns a fresh sidecar.
#[tauri::command]
pub fn start_backend(app: AppHandle, state: State<LauncherState>) -> Result<(), String> {
    use crate::launcher::backend::spawn_sidecar;
    use tauri::Manager;

    if state.lock().unwrap().attached {
        return Err("backend not owned by launcher".into());
    }

    let (plugins_dir, data_dir, rl_addr) = {
        let ctx = state.lock().unwrap();
        let s = ctx.settings.load();
        (s.plugins_dir.clone(), s.data_dir.clone(), s.rl_addr.clone())
    };

    if let Some(handle) = app.try_state::<crate::launcher::BackendHandle>() {
        let mut slot = handle.0.lock().unwrap();
        if let Some(mut owned) = slot.take() {
            owned.terminate(std::time::Duration::from_secs(2));
        }
        let log_path = crate::paths::launcher_log_path();
        match spawn_sidecar(&app, log_path, plugins_dir, data_dir, rl_addr) {
            Ok(new_owned) => {
                *slot = Some(new_owned);
                drop(slot);
                state.lock().unwrap().stopped_by_user = false;
            }
            Err(e) => return Err(e),
        }
    }
    Ok(())
}

#[tauri::command]
pub fn open_data_folder(app: AppHandle, state: State<LauncherState>) -> Result<(), String> {
    use tauri_plugin_opener::OpenerExt;

    // Prefer the user's configured data_dir; fall back to the OS-standard
    // data location used elsewhere in the app.
    let configured = {
        let ctx = state.lock().unwrap();
        ctx.settings.load().data_dir
    };

    let path = match configured.filter(|s| !s.trim().is_empty()) {
        Some(s) => std::path::PathBuf::from(s),
        None => crate::paths::default_data_dir(),
    };

    // Make sure the directory exists; the OS file manager errors out
    // ungracefully on a non-existent path.
    if !path.exists() {
        std::fs::create_dir_all(&path).map_err(|e| {
            format!("create data folder {}: {e}", path.display())
        })?;
    }

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

/// Open an arbitrary URL in the user's default browser. Used by the
/// dashboard iframe to route `target="_blank"` clicks (Open buttons,
/// API endpoint links, etc.) out of the webview and into a real browser.
/// Restricted to http/https schemes so a malicious dashboard payload
/// can't pop open `file://` or shell schemes.
#[tauri::command]
pub fn open_external_url(app: AppHandle, url: String) -> Result<(), String> {
    use tauri_plugin_opener::OpenerExt;
    if !(url.starts_with("http://") || url.starts_with("https://")) {
        return Err("only http/https URLs allowed".into());
    }
    app.opener().open_url(url, None::<&str>).map_err(|e| e.to_string())
}

#[tauri::command]
pub fn quit(app: AppHandle) {
    app.exit(0);
}

#[derive(serde::Serialize, serde::Deserialize)]
pub struct LauncherSettingsView {
    pub plugins_dir: Option<String>,
    pub data_dir: Option<String>,
    pub rl_addr: Option<String>,
    /// Resolved default paths shown as placeholders in the Settings UI
    /// when the user hasn't overridden them. Computed once per call so
    /// the dialog can show e.g. "C:\Users\Foo\AppData\Local\RLToolkit\plugins"
    /// instead of a vague "default" string.
    pub default_plugins_dir: String,
    pub default_data_dir: String,
}

#[tauri::command]
pub fn get_settings(state: State<LauncherState>) -> LauncherSettingsView {
    let ctx = state.lock().unwrap();
    let s = ctx.settings.load();
    LauncherSettingsView {
        plugins_dir: s.plugins_dir,
        data_dir: s.data_dir,
        rl_addr: s.rl_addr,
        default_plugins_dir: crate::paths::default_plugins_dir()
            .to_string_lossy()
            .into_owned(),
        default_data_dir: crate::paths::default_data_dir()
            .to_string_lossy()
            .into_owned(),
    }
}

#[tauri::command]
pub fn save_settings(
    plugins_dir: Option<String>,
    data_dir: Option<String>,
    rl_addr: Option<String>,
    app: AppHandle,
    state: State<LauncherState>,
) -> Result<bool, String> {
    use crate::launcher::backend::spawn_sidecar;

    // Persist to disk.
    let attached = {
        let ctx = state.lock().unwrap();
        let mut s = ctx.settings.load();
        s.plugins_dir = plugins_dir.clone().filter(|p| !p.trim().is_empty());
        s.data_dir = data_dir.clone().filter(|p| !p.trim().is_empty());
        s.rl_addr = rl_addr.clone().filter(|p| !p.trim().is_empty());
        ctx.settings.save(&s).map_err(|e| e.to_string())?;
        ctx.attached
    };

    // If we don't own the backend, just signal that the user must restart.
    if attached {
        return Ok(false);
    }

    // Otherwise kill+respawn the sidecar with the new flags.
    use tauri::Manager;
    if let Some(handle) = app.try_state::<crate::launcher::BackendHandle>() {
        let mut slot = handle.0.lock().unwrap();
        if let Some(mut owned) = slot.take() {
            owned.terminate(std::time::Duration::from_secs(2));
        }
        let log_path = crate::paths::launcher_log_path();
        match spawn_sidecar(&app, log_path, plugins_dir, data_dir, rl_addr) {
            Ok(new_owned) => *slot = Some(new_owned),
            Err(e) => return Err(e),
        }
    }
    Ok(true)
}
