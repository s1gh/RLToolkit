//! Auto-update for the launcher. Compiled in only with the
//! `bundled-updater` feature (Windows NSIS, Linux AppImage); portable
//! builds omit it because tauri-plugin-updater deserializes config
//! eagerly and the plugin can't be left "configured but inactive".
//!
//! Surface: check_for_updates / apply_update (frontend commands),
//! check_on_startup (best-effort, emits `updater://available`).

#![cfg(feature = "bundled-updater")]

use serde::Serialize;
use tauri::AppHandle;
use tauri_plugin_updater::UpdaterExt;

/// Result of a check, suitable for return to the frontend webview.
#[derive(Serialize)]
#[serde(tag = "kind", rename_all = "snake_case")]
pub enum CheckResult {
    /// An update is available. Caller can show a banner and call
    /// apply_update when the user accepts.
    Available {
        version: String,
        notes: Option<String>,
    },
    UpToDate,
    /// Updater not configured (portable build). Frontend should fall
    /// back to "Open release page".
    Unavailable { reason: String },
    /// Transient (network or signature) error.
    Error { reason: String },
}

/// Frontend-callable: ask whether an update exists.
#[tauri::command]
pub async fn check_for_updates(app: AppHandle) -> CheckResult {
    do_check(&app).await
}

/// Download and install the pending update. The updater overwrites
/// the running bundle in place; the OS won't allow that while the
/// sidecar is alive, so `on_before_exit` drains it first.
/// `cleanup_before_exit()` is called explicitly because overriding
/// `on_before_exit` replaces the plugin's default tray/window cleanup.
#[tauri::command]
pub async fn apply_update(app: AppHandle) -> Result<(), String> {
    use tauri_plugin_updater::UpdaterExt;

    let app_for_hook = app.clone();
    let updater = app
        .updater_builder()
        .on_before_exit(move || {
            // Drain the sidecar so NSIS can overwrite rl-toolkit.exe,
            // then run Tauri's standard tray/window cleanup.
            use tauri::Manager;
            if let Some(state) = app_for_hook.try_state::<crate::launcher::BackendHandle>() {
                if let Some(mut owned) = state.0.lock().unwrap().take() {
                    crate::log_info!("[updater] terminating sidecar before installer runs");
                    owned.terminate(std::time::Duration::from_secs(2));
                }
            }
            app_for_hook.cleanup_before_exit();
        })
        .build()
        .map_err(|e| format!("updater unavailable: {e}"))?;
    let update = updater.check().await.map_err(|e| format!("check failed: {e}"))?;
    let Some(update) = update else {
        return Err("no update available".to_string());
    };
    update
        .download_and_install(|_, _| {}, || {})
        .await
        .map_err(|e| format!("install failed: {e}"))?;

    // Windows: the current process exits inside download_and_install
    // (via on_before_exit + process::exit) and the new NSIS installer
    // relaunches us, so this point is unreachable on success.
    //
    // Linux: download_and_install rewrites the AppImage in place and
    // returns Ok — restart explicitly so the new process loads the
    // updated bundle. app.restart() execs and never returns.
    #[cfg(target_os = "linux")]
    {
        crate::log_info!("[updater] install complete; restarting");
        app.restart();
    }

    #[cfg(not(target_os = "linux"))]
    return Ok(());

    #[cfg(target_os = "linux")]
    #[allow(unreachable_code)]
    Ok(())
}

/// Best-effort startup check.
pub async fn check_on_startup(app: AppHandle) {
    match do_check(&app).await {
        CheckResult::Available { version, .. } => {
            crate::log_info!("[updater] update available: {version}");
            use tauri::Emitter;
            let _ = app.emit("updater://available", version);
        }
        CheckResult::UpToDate => {
            crate::log_debug!("[updater] up to date");
        }
        CheckResult::Unavailable { reason } => {
            crate::log_debug!("[updater] not configured: {reason}");
        }
        CheckResult::Error { reason } => {
            crate::log_debug!("[updater] check error: {reason}");
        }
    }
}

async fn do_check(app: &AppHandle) -> CheckResult {
    let updater = match app.updater() {
        Ok(u) => u,
        Err(e) => {
            return CheckResult::Unavailable {
                reason: e.to_string(),
            };
        }
    };
    match updater.check().await {
        Ok(Some(update)) => CheckResult::Available {
            version: update.version.clone(),
            notes: update.body.clone(),
        },
        Ok(None) => CheckResult::UpToDate,
        Err(e) => CheckResult::Error {
            reason: e.to_string(),
        },
    }
}
