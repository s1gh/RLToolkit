pub mod backend;
pub mod ipc;
pub mod settings;
pub mod tray;
#[cfg(feature = "installer-updater")]
pub mod updater;
pub mod window;

#[cfg(test)]
mod tests;

use crate::cli::Args;
use crate::focus_watcher;
use ipc::{LauncherCtx, LauncherState};
use settings::SettingsStore;
use std::sync::Mutex;
use tauri::{Builder, Manager};

pub fn install_plugins<R: tauri::Runtime>(builder: Builder<R>) -> Builder<R> {
    let builder = builder
        .plugin(tauri_plugin_shell::init())
        .plugin(tauri_plugin_opener::init())
        .plugin(tauri_plugin_dialog::init());

    #[cfg(feature = "installer-updater")]
    let builder = builder.plugin(tauri_plugin_updater::Builder::new().build());

    builder.plugin(tauri_plugin_single_instance::init(|app, _argv, _cwd| {
        if let Some(w) = app.get_webview_window("launcher") {
            let _ = w.unminimize();
            let _ = w.show();
            let _ = w.set_focus();
        }
    }))
}

pub fn run(args: Args) {
    // Initialize file logging before anything else can fail. The
    // launcher settings file lives next to the data dir, but the
    // *configured* data_dir may differ (user override) — we resolve
    // logs against the active data_dir from settings, falling back to
    // the platform default. Either way the user finds them under
    // `<data dir>/logs/`.
    let settings_store = SettingsStore::new(crate::paths::launcher_settings_path());
    let initial = settings_store.load();
    let data_dir_for_logs = initial
        .data_dir
        .as_ref()
        .filter(|p| !p.trim().is_empty())
        .map(std::path::PathBuf::from)
        .unwrap_or_else(crate::paths::default_data_dir);
    crate::logging::init(data_dir_for_logs);
    crate::log_info!("[launcher] starting (toolkit={})", args.toolkit);

    let ctx = LauncherCtx {
        toolkit_url: args.toolkit.trim_end_matches('/').to_string(),
        settings: settings_store,
        overlay_enabled: initial.overlay_enabled,
        attached: false,
        starting: true,
        tray_ok: true,
        stopped_by_user: false,
    };

    let builder = install_plugins(tauri::Builder::default())
        .manage::<LauncherState>(Mutex::new(ctx));

    #[cfg(feature = "installer-updater")]
    let builder = builder.invoke_handler(tauri::generate_handler![
        ipc::get_status,
        ipc::get_toolkit_url,
        ipc::get_app_version,
        ipc::toggle_overlay,
        ipc::restart_backend,
        ipc::stop_backend,
        ipc::start_backend,
        ipc::open_data_folder,
        ipc::open_dashboard_in_browser,
        ipc::open_external_url,
        ipc::quit,
        ipc::get_settings,
        ipc::save_settings,
        ipc::get_launcher_monitor_size,
        updater::check_for_updates,
        updater::apply_update,
    ]);

    #[cfg(not(feature = "installer-updater"))]
    let builder = builder.invoke_handler(tauri::generate_handler![
        ipc::get_status,
        ipc::get_toolkit_url,
        ipc::get_app_version,
        ipc::toggle_overlay,
        ipc::restart_backend,
        ipc::stop_backend,
        ipc::start_backend,
        ipc::open_data_folder,
        ipc::open_dashboard_in_browser,
        ipc::open_external_url,
        ipc::quit,
        ipc::get_settings,
        ipc::save_settings,
        ipc::get_launcher_monitor_size,
    ]);

    builder
        .setup(move |app| {
            let handle = app.handle().clone();
            let (toolkit_url, initial_settings) = {
                let state: tauri::State<LauncherState> = app.state();
                let ctx = state.lock().unwrap();
                let url = ctx.toolkit_url.clone();
                let settings = ctx.settings.load();
                (url, settings)
            };

            // Build the window immediately so the user sees something while we probe.
            window::build_launcher_window(&handle, &toolkit_url, &initial_settings)?;

            // Build the overlay window now too, hidden. It must be created
            // from setup() (the Tauri main thread during initialization).
            // Building it later from an IPC command's worker thread —
            // which is what toggle_overlay used to do — deadlocks
            // WebView2 on Windows: build() never returns and every
            // subsequent invoke from the launcher webview hangs forever.
            // Creating it once up front means toggle_overlay only needs
            // show()/hide(), which is safe from any thread.
            if let Err(e) = crate::overlay_bridge::ensure_overlay(&handle) {
                crate::log_error!("[launcher] overlay pre-build failed: {e}");
            }

            // Best-effort update check. Compiled in only for the
            // installer build — the portable build omits the updater
            // entirely (the plugin can't be left "configured but
            // inactive"; its setup deserializes config eagerly).
            #[cfg(feature = "installer-updater")]
            {
                let handle_for_updater = handle.clone();
                tauri::async_runtime::spawn(async move {
                    updater::check_on_startup(handle_for_updater).await;
                });
            }

            // Drive real RL focus events into every webview the launcher
            // owns (the launcher window itself ignores them; the overlay
            // window forwards them to plugin iframes for hide_when_unfocused).
            let rule = focus_watcher::match_rule_from_arg(args.game_match.as_deref());
            focus_watcher::spawn(handle.clone(), rule);

            if let Err(e) = tray::setup_tray(&handle, "RL Toolkit") {
                crate::log_warn!("[launcher] tray failed: {e}");
                if let Some(state) = handle.try_state::<LauncherState>() {
                    state.lock().unwrap().tray_ok = false;
                }
            }

            // Run probe → spawn off the main thread so the UI stays responsive.
            let app_for_probe = handle.clone();
            std::thread::spawn(move || {
                use crate::launcher::backend::{probe_status, spawn_sidecar, BackendOwnership, ProbeOutcome};
                let log_path = crate::launcher::backend::sidecar_log_path_today();
                let (plugins_dir, data_dir, rl_addr) = {
                    use tauri::Manager;
                    let state: tauri::State<LauncherState> = app_for_probe.state();
                    let ctx = state.lock().unwrap();
                    let s = ctx.settings.load();
                    (s.plugins_dir.clone(), s.data_dir.clone(), s.rl_addr.clone())
                };
                let url = format!("{}/api/status", toolkit_url.trim_end_matches('/'));
                let outcome = probe_status(&url, std::time::Duration::from_millis(500));

                let owned = match outcome {
                    ProbeOutcome::Toolkit { .. } => {
                        set_attached(&app_for_probe, true);
                        BackendOwnership::Attached
                    }
                    ProbeOutcome::Unreachable => {
                        match spawn_sidecar(&app_for_probe, log_path.clone(), plugins_dir, data_dir, rl_addr) {
                            Ok(b) => {
                                set_attached(&app_for_probe, false);
                                b
                            }
                            Err(e) => {
                                crate::log_error!("[launcher] spawn failed: {e}");
                                BackendOwnership::Unavailable
                            }
                        }
                    }
                    ProbeOutcome::Unrelated => {
                        crate::log_warn!("[launcher] something else is on the toolkit port");
                        BackendOwnership::Unavailable
                    }
                };

                // Wait for ready (cap 10 s) when we just spawned.
                if matches!(owned, BackendOwnership::SpawnedSidecar(_) | BackendOwnership::SpawnedRaw(_)) {
                    let deadline = std::time::Instant::now() + std::time::Duration::from_secs(10);
                    while std::time::Instant::now() < deadline {
                        if matches!(probe_status(&url, std::time::Duration::from_millis(300)), ProbeOutcome::Toolkit { .. }) {
                            break;
                        }
                        std::thread::sleep(std::time::Duration::from_millis(200));
                    }
                }
                clear_starting(&app_for_probe);

                // Report the launcher window's monitor logical size as the
                // detected overlay surface. The overlay window may not be
                // mounted yet (or ever, this session) — without this report
                // the backend has no idea what resolution the user actually
                // runs, and falls back to 1920×1080 in the Settings UI.
                // The overlay's own report (when it does mount) will
                // overwrite this with its own monitor's size, which is the
                // right behavior if the user pinned the overlay to a
                // specific display.
                if matches!(owned, BackendOwnership::Attached
                    | BackendOwnership::SpawnedSidecar(_)
                    | BackendOwnership::SpawnedRaw(_))
                {
                    if let Some(win) = app_for_probe.get_webview_window("launcher") {
                        if let Some((w, h)) = window::window_monitor_logical(&win) {
                            report_detected_surface(&toolkit_url, w, h);
                        }
                    }
                }

                // Autostart the overlay if overlay_enabled was set.
                // The overlay window was already built (hidden) during
                // setup(); just reveal it.
                let auto = {
                    use tauri::Manager;
                    let state: tauri::State<LauncherState> = app_for_probe.state();
                    let ctx = state.lock().unwrap();
                    ctx.overlay_enabled
                };
                if auto {
                    use tauri::Manager;
                    if let Some(w) = app_for_probe.get_webview_window("main") {
                        let _ = w.show();
                    }
                }

                // Park the ownership in app state so quit can drain it.
                let owned = std::sync::Arc::new(std::sync::Mutex::new(Some(owned)));
                app_for_probe.manage(BackendHandle(owned));
            });

            Ok(())
        })
        .on_window_event(|window, event| {
            if window.label() == "launcher" {
                if let tauri::WindowEvent::CloseRequested { api, .. } = event {
                    let tray_ok = window
                        .app_handle()
                        .try_state::<LauncherState>()
                        .map(|s| s.lock().unwrap().tray_ok)
                        .unwrap_or(true);
                    if tray_ok {
                        api.prevent_close();
                        let _ = window.hide();
                    }
                    // else: allow close → fires ExitRequested → backend drained.
                }
            }
        })
        .build(tauri::generate_context!())
        .expect("error while building rl-widget launcher")
        .run(|app_handle, event| {
            if let tauri::RunEvent::ExitRequested { .. } = event {
                if let Some(state) = app_handle.try_state::<BackendHandle>() {
                    if let Some(mut owned) = state.0.lock().unwrap().take() {
                        owned.terminate(std::time::Duration::from_secs(2));
                    }
                }
            }
        });
}

pub struct BackendHandle(pub std::sync::Arc<std::sync::Mutex<Option<backend::BackendOwnership>>>);

fn set_attached(app: &tauri::AppHandle, attached: bool) {
    use tauri::Manager;
    if let Some(state) = app.try_state::<LauncherState>() {
        let mut ctx = state.lock().unwrap();
        ctx.attached = attached;
    }
}

fn clear_starting(app: &tauri::AppHandle) {
    use tauri::Manager;
    if let Some(state) = app.try_state::<LauncherState>() {
        let mut ctx = state.lock().unwrap();
        ctx.starting = false;
    }
}

/// Best-effort POST of the launcher window's bound monitor logical size
/// to /api/overlay/surface/detected. Mirrors the rl-widget overlay's
/// detection report so the backend has a sane "detected" value even
/// when the overlay isn't mounted. Failures are logged but never
/// propagated — the backend may not be ready at this exact moment, and
/// the UI still works without a detected value (fallback to 1920×1080).
fn report_detected_surface(toolkit: &str, w: f64, h: f64) {
    let base = toolkit.trim_end_matches('/');
    let url = format!("{}/api/overlay/surface/detected", base);
    let body = format!(
        r#"{{"width":{},"height":{}}}"#,
        w.round() as i64,
        h.round() as i64,
    );
    let result = ureq::post(&url)
        .timeout(std::time::Duration::from_secs(2))
        .set("Content-Type", "application/json")
        .send_string(&body);
    if let Err(e) = result {
        // Debug-only: this fires during the brief window between
        // backend probe success and the HTTP server actually
        // accepting connections. Surfacing it on stderr panicked
        // beta users who thought the launcher was broken; the file
        // log keeps the trail for genuine bug hunts.
        crate::log_debug!("[launcher] detected-surface report failed: {e}");
    }
}
