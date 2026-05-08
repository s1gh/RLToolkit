//! Auto-update for the launcher (installer build only).
//!
//! Two paths:
//!   - check_on_startup: best-effort, called from run() after the
//!     window is built. Silent on no-update or any error.
//!   - check_for_updates command: invoked from a "Check for updates"
//!     menu item in the frontend. Always reports back.
//!
//! In the portable build, tauri-plugin-updater is still compiled in
//! but the `plugins.updater` block is absent from the active Tauri
//! config; UpdaterExt::updater() returns Err and we surface that
//! verbatim so the frontend can degrade to "open release page."

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
    /// We are on the latest version.
    UpToDate,
    /// Updater not configured for this build (typical of portable).
    /// Frontend should fall back to "Open release page."
    Unavailable { reason: String },
    /// Network or signature error. Treat as transient.
    Error { reason: String },
}

/// Frontend-callable: ask whether an update exists.
#[tauri::command]
pub async fn check_for_updates(app: AppHandle) -> CheckResult {
    do_check(&app).await
}

/// Frontend-callable: download and install the pending update.
/// Tauri's updater handles signature verification, download, running
/// the new NSIS installer silently, and relaunching. We surface only
/// success/failure here.
#[tauri::command]
pub async fn apply_update(app: AppHandle) -> Result<(), String> {
    let updater = app.updater().map_err(|e| format!("updater unavailable: {e}"))?;
    let update = updater.check().await.map_err(|e| format!("check failed: {e}"))?;
    let Some(update) = update else {
        return Err("no update available".to_string());
    };
    update
        .download_and_install(|_, _| {}, || {})
        .await
        .map_err(|e| format!("install failed: {e}"))?;
    // Tauri restarts the app once the installer relaunches it.
    // Nothing more to do here.
    Ok(())
}

/// Best-effort startup check. Logs and returns; never panics.
pub async fn check_on_startup(app: AppHandle) {
    match do_check(&app).await {
        CheckResult::Available { version, .. } => {
            crate::log_info!("[updater] update available: {version}");
            // Frontend listens for this event and shows the banner.
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
            // Network blips are normal; debug-level only.
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
