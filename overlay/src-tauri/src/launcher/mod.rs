pub mod backend;
pub mod edit_mode;
pub mod ipc;
pub mod job_object;
pub mod settings;
pub mod shortcut;
pub mod tray;
#[cfg(feature = "bundled-updater")]
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
        .plugin(tauri_plugin_dialog::init())
        .plugin(tauri_plugin_global_shortcut::Builder::new().build());

    #[cfg(feature = "bundled-updater")]
    let builder = builder.plugin(tauri_plugin_updater::Builder::new().build());

    builder.plugin(tauri_plugin_single_instance::init(|app, argv, _cwd| {
        if argv.iter().any(|a| a == "--toggle-edit") {
            if let Err(e) = crate::launcher::edit_mode::toggle(app) {
                crate::log_warn!("[overlay-edit] toggle failed: {e}");
            }
            return;
        }
        if let Some(w) = app.get_webview_window("launcher") {
            let _ = w.unminimize();
            let _ = w.show();
            let _ = w.set_focus();
        }
    }))
}

pub fn run(args: Args) {
    // Initialize file logging early. The user can override data_dir via
    // settings, so resolve logs against the configured value (falling
    // back to the platform default). Logs always end up at
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
        overlay_loaded: false,
    };

    let builder = install_plugins(tauri::Builder::default())
        .manage::<LauncherState>(Mutex::new(ctx))
        .manage(crate::launcher::edit_mode::EditModeState::<tauri::Wry>::default());

    #[cfg(feature = "bundled-updater")]
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
        ipc::overlay_edit_toggle,
        ipc::overlay_edit_exit,
        ipc::overlay_edit_module_failed,
        ipc::get_edit_mode_shortcut,
        ipc::set_edit_mode_shortcut,
        updater::check_for_updates,
        updater::apply_update,
    ]);

    #[cfg(not(feature = "bundled-updater"))]
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
        ipc::overlay_edit_toggle,
        ipc::overlay_edit_exit,
        ipc::overlay_edit_module_failed,
        ipc::get_edit_mode_shortcut,
        ipc::set_edit_mode_shortcut,
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

            // Build the launcher window immediately so the user sees
            // something while we probe.
            window::build_launcher_window(&handle, &toolkit_url, &initial_settings)?;

            // Build the overlay window hidden. It MUST be created from
            // setup() (the Tauri main thread); creating it later from
            // an IPC worker deadlocks WebView2 on Windows. With the
            // window pre-built, toggle_overlay only needs show()/hide(),
            // which is safe from any thread.
            if let Err(e) = crate::overlay_bridge::ensure_overlay(&handle) {
                crate::log_error!("[launcher] overlay pre-build failed: {e}");
            }

            // Update check, installer build only. The plugin's setup
            // deserializes config eagerly, so it can't be left
            // "configured but inactive" in the portable build.
            #[cfg(feature = "bundled-updater")]
            {
                let handle_for_updater = handle.clone();
                tauri::async_runtime::spawn(async move {
                    updater::check_on_startup(handle_for_updater).await;
                });
            }

            // Forward RL focus events to every webview. The launcher
            // window ignores them; the overlay window forwards them
            // to plugin iframes for hide_when_unfocused.
            let rule = focus_watcher::match_rule_from_arg(args.game_match.as_deref());
            focus_watcher::spawn(handle.clone(), rule);

            crate::launcher::shortcut::register(&handle);

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

                // Decide whether the backend is actually serving before
                // we touch the overlay webview. Attached = already up at
                // first probe; SpawnedSidecar/Raw = we just launched it
                // and need to wait for /api/status to start answering.
                // A 10s ceiling means a backend that hangs on boot
                // doesn't keep the overlay hidden forever; toggle_overlay
                // has a lazy-navigate fallback for the "backend came up
                // after the window closed" case.
                let backend_ready = match &owned {
                    BackendOwnership::Attached => true,
                    BackendOwnership::SpawnedSidecar(_) | BackendOwnership::SpawnedRaw(_) => {
                        let deadline = std::time::Instant::now() + std::time::Duration::from_secs(10);
                        let mut up = false;
                        while std::time::Instant::now() < deadline {
                            if matches!(
                                probe_status(&url, std::time::Duration::from_millis(300)),
                                ProbeOutcome::Toolkit { .. }
                            ) {
                                up = true;
                                break;
                            }
                            std::thread::sleep(std::time::Duration::from_millis(200));
                        }
                        up
                    }
                    _ => false,
                };
                clear_starting(&app_for_probe);

                // Report the launcher's monitor size as the detected
                // surface. Without this, the backend has no idea what
                // resolution the user runs and falls back to 1920×1080
                // in the Settings UI. The overlay's own report (when
                // it mounts) overwrites this with the overlay's monitor.
                // No point reporting if the backend can't be reached.
                if backend_ready {
                    if let Some(win) = app_for_probe.get_webview_window("launcher") {
                        if let Some((w, h)) = window::window_monitor_logical(&win) {
                            report_detected_surface(&toolkit_url, w, h);
                        }
                    }
                }

                // Live-load + autostart, only on confirmed readiness.
                // The overlay window was built with a transparent local
                // placeholder (overlay/src/index.html) so any stray show
                // — winit's set_fullscreen does briefly map the HWND on
                // Windows even with .visible(false) — renders nothing
                // visible instead of WebView2's "can't connect" error
                // page filling the screen. Navigating now replaces the
                // placeholder with the live /overlay aggregator; show
                // happens after, so the user only sees the real overlay.
                if backend_ready {
                    match crate::overlay_bridge::navigate_overlay(&app_for_probe, &toolkit_url) {
                        Ok(()) => {
                            use tauri::Manager;
                            if let Some(state) = app_for_probe.try_state::<LauncherState>() {
                                state.lock().unwrap().overlay_loaded = true;
                            }
                        }
                        Err(e) => crate::log_warn!("[launcher] overlay navigate failed: {e}"),
                    }

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
                }

                // Park ownership in app state so quit can drain it.
                let owned = std::sync::Arc::new(std::sync::Mutex::new(Some(owned)));
                app_for_probe.manage(BackendHandle(owned));
            });

            Ok(())
        })
        .on_window_event(|window, event| {
            if window.label() == "launcher" {
                if let tauri::WindowEvent::CloseRequested { api, .. } = event {
                    // On Linux, treat a close request as a real close.
                    // Many Linux compositors (Hyprland, Sway, etc.) ship
                    // without a system tray, so hide-to-tray orphans the
                    // process with no way to reach it. Win+Q / wmctrl
                    // -c / clicking X all flow through here and the user
                    // has no other recourse if we swallow the event.
                    // Windows and macOS keep the tray-hide convention.
                    #[cfg(not(target_os = "linux"))]
                    {
                        let tray_ok = window
                            .app_handle()
                            .try_state::<LauncherState>()
                            .map(|s| s.lock().unwrap().tray_ok)
                            .unwrap_or(true);
                        if tray_ok {
                            api.prevent_close();
                            let _ = window.hide();
                        }
                        // No tray: allow close → ExitRequested → backend drained.
                    }
                    #[cfg(target_os = "linux")]
                    {
                        // Allow the close to proceed AND request app
                        // exit. Tauri 2 doesn't auto-exit when the last
                        // window closes (no AppKit-style "active app
                        // without windows" concept on Linux either,
                        // but Tauri leaves the process alive anyway),
                        // so without this the launcher window vanishes
                        // and the rl-widget process keeps running
                        // headless. exit(0) flows through the
                        // RunEvent::ExitRequested handler below, which
                        // drains the backend sidecar before tearing
                        // the runtime down.
                        let _ = api;
                        window.app_handle().exit(0);
                    }
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

/// Best-effort POST of the launcher window's monitor size to
/// /api/overlay/surface/detected. Mirrors the overlay's detection
/// report so the backend has a sane "detected" value even when the
/// overlay isn't mounted. Failures are logged at debug level only.
fn report_detected_surface(toolkit: &str, w: f64, h: f64) {
    let base = toolkit.trim_end_matches('/');
    let url = format!("{}/api/overlay/surface/detected", base);
    let body = format!(
        r#"{{"width":{},"height":{}}}"#,
        w.round() as i64,
        h.round() as i64,
    );
    let result = ureq::post(&url)
        .config()
        .timeout_global(Some(std::time::Duration::from_secs(2)))
        .build()
        .content_type("application/json")
        .send(&body);
    if let Err(e) = result {
        // Debug-only: fires in the brief window between probe success
        // and the HTTP server accepting connections. Stderr noise here
        // alarms users into thinking the launcher is broken.
        crate::log_debug!("[launcher] detected-surface report failed: {e}");
    }
}
