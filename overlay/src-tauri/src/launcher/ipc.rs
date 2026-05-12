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
    /// True when the user explicitly stopped via Stop, so the UI shows
    /// "Start backend" instead of "Restart" (distinct from "crashed").
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
pub fn get_app_version() -> &'static str {
    env!("CARGO_PKG_VERSION")
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
    }

    // The overlay window was built once during launcher setup() (see
    // mod.rs) — building from an IPC worker deadlocks WebView2 on
    // Windows. show()/hide() is safe from any thread.
    if let Some(w) = app.get_webview_window("main") {
        if enabled {
            let _ = w.show();
        } else {
            // Pre-hide flush. WebKitGTK doesn't paint while a document
            // is hidden (Page Visibility / "update the rendering"
            // gating), so any DOM mutation that happens between hide
            // and show won't reach the buffer GTK hands the compositor
            // on the post-show map. Result: stale plugin contents
            // briefly flash on remap if the user disabled a plugin
            // while the overlay was off. Mitigate by clearing the
            // overlay's iframes BEFORE hide() — while the document
            // is still visible, so the clear actually paints — then
            // wait a beat for that paint to land before unmapping.
            //
            // The aggregator (web/overlay.html) installs
            // window.__rlt_prepare_for_hide which unmounts every
            // iframe and resolves after two requestAnimationFrame
            // ticks. We invoke it via fire-and-forget eval, then
            // sleep a generous fixed budget (60ms ≈ 4 frames at
            // 60Hz) to cover the rAF chain plus the GTK frame-clock
            // commit, and only then call hide(). On show() we don't
            // need to do anything special — the aggregator listens
            // for visibilitychange and re-applies lastMerged.
            let _ = w.eval(
                "if (typeof window.__rlt_prepare_for_hide === 'function') \
                 { window.__rlt_prepare_for_hide(); }",
            );
            let w_clone = w.clone();
            std::thread::spawn(move || {
                std::thread::sleep(std::time::Duration::from_millis(60));
                let _ = w_clone.hide();
            });
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
        let log_path = crate::launcher::backend::sidecar_log_path_today();
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
        let log_path = crate::launcher::backend::sidecar_log_path_today();
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

    let configured = {
        let ctx = state.lock().unwrap();
        ctx.settings.load().data_dir
    };

    let path = match configured.filter(|s| !s.trim().is_empty()) {
        Some(s) => std::path::PathBuf::from(s),
        None => crate::paths::default_data_dir(),
    };

    // Pre-create the directory; the OS file manager errors ungracefully
    // on a non-existent path.
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

/// Open an arbitrary URL in the user's default browser. Restricted to
/// http/https so a malicious dashboard payload can't pop file:// or
/// shell schemes.
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

/// Logical size of the launcher window's bound monitor. Used by the
/// Settings modal to re-post the detected surface so it stays fresh
/// after Clear and across monitor changes.
#[derive(serde::Serialize)]
pub struct MonitorSize {
    pub width: u32,
    pub height: u32,
}

#[tauri::command]
pub fn get_launcher_monitor_size(app: AppHandle) -> Option<MonitorSize> {
    use tauri::Manager;
    let win = app.get_webview_window("launcher")?;
    let (w, h) = crate::launcher::window::window_monitor_logical(&win)?;
    Some(MonitorSize {
        width: w.round() as u32,
        height: h.round() as u32,
    })
}

#[derive(serde::Serialize, serde::Deserialize)]
pub struct LauncherSettingsView {
    pub plugins_dir: Option<String>,
    pub data_dir: Option<String>,
    pub rl_addr: Option<String>,
    /// Resolved default paths shown as placeholders in the Settings UI
    /// when the user hasn't overridden them.
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

/// Outcome of save_settings:
/// - changed=false: nothing to do; modal can close.
/// - changed=true, respawned=false: attached mode; user must restart
///   the backend manually.
/// - changed=true, respawned=true: sidecar killed and respawned with
///   the new flags; UI should reload the dashboard.
#[derive(serde::Serialize)]
pub struct SaveSettingsResult {
    pub changed: bool,
    pub respawned: bool,
}

#[tauri::command]
pub fn save_settings(
    plugins_dir: Option<String>,
    data_dir: Option<String>,
    rl_addr: Option<String>,
    app: AppHandle,
    state: State<LauncherState>,
) -> Result<SaveSettingsResult, String> {
    use crate::launcher::backend::spawn_sidecar;

    // Normalize: empty strings are treated as "use default", same as None.
    let new_plugins = plugins_dir.clone().filter(|p| !p.trim().is_empty());
    let new_data = data_dir.clone().filter(|p| !p.trim().is_empty());
    let new_rl = rl_addr.clone().filter(|p| !p.trim().is_empty());

    // Detect whether any backend-affecting value actually changed,
    // otherwise every Save (no edits, or surface-only edits) would
    // kill + respawn the sidecar and flash the dashboard.
    let (attached, changed) = {
        let ctx = state.lock().unwrap();
        let prev = ctx.settings.load();
        let changed = prev.plugins_dir != new_plugins
            || prev.data_dir != new_data
            || prev.rl_addr != new_rl;
        let mut s = prev;
        s.plugins_dir = new_plugins;
        s.data_dir = new_data;
        s.rl_addr = new_rl;
        ctx.settings.save(&s).map_err(|e| e.to_string())?;
        (ctx.attached, changed)
    };

    if !changed {
        return Ok(SaveSettingsResult { changed: false, respawned: false });
    }
    if attached {
        return Ok(SaveSettingsResult { changed: true, respawned: false });
    }

    // Launcher-owned backend: kill+respawn the sidecar with the new flags.
    use tauri::Manager;
    if let Some(handle) = app.try_state::<crate::launcher::BackendHandle>() {
        let mut slot = handle.0.lock().unwrap();
        if let Some(mut owned) = slot.take() {
            owned.terminate(std::time::Duration::from_secs(2));
        }
        let log_path = crate::launcher::backend::sidecar_log_path_today();
        match spawn_sidecar(&app, log_path, plugins_dir, data_dir, rl_addr) {
            Ok(new_owned) => *slot = Some(new_owned),
            Err(e) => return Err(e),
        }
    }
    Ok(SaveSettingsResult { changed: true, respawned: true })
}

#[tauri::command]
pub fn overlay_edit_toggle(app: AppHandle) -> Result<bool, String> {
    crate::launcher::edit_mode::toggle(&app)
}

#[tauri::command]
pub fn overlay_edit_exit(app: AppHandle) -> Result<bool, String> {
    crate::launcher::edit_mode::set(&app, false)
}

#[tauri::command]
pub fn overlay_edit_module_failed(app: AppHandle, reason: String) -> Result<(), String> {
    crate::log_warn!("[overlay-edit] live-edit module load failed: {reason}");
    // Module load failed; force off so we're not stuck with click-through disabled.
    let _ = crate::launcher::edit_mode::set(&app, false);
    Ok(())
}
