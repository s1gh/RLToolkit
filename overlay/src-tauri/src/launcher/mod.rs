pub mod backend;
pub mod ipc;
pub mod settings;
pub mod tray;
pub mod window;

#[cfg(test)]
mod tests;

use crate::cli::Args;
use ipc::{LauncherCtx, LauncherState};
use settings::SettingsStore;
use std::sync::Mutex;
use tauri::{Builder, Manager};

pub fn install_plugins<R: tauri::Runtime>(builder: Builder<R>) -> Builder<R> {
    builder
        .plugin(tauri_plugin_shell::init())
        .plugin(tauri_plugin_opener::init())
        .plugin(tauri_plugin_dialog::init())
        .plugin(tauri_plugin_single_instance::init(|app, _argv, _cwd| {
            if let Some(w) = app.get_webview_window("launcher") {
                let _ = w.unminimize();
                let _ = w.show();
                let _ = w.set_focus();
            }
        }))
}

pub fn run(args: Args) {
    let settings_path = std::env::current_dir()
        .unwrap_or_else(|_| std::path::PathBuf::from("."))
        .join("data")
        .join("launcher.json");
    let settings_store = SettingsStore::new(settings_path);
    let initial = settings_store.load();

    let ctx = LauncherCtx {
        toolkit_url: args.toolkit.trim_end_matches('/').to_string(),
        settings: settings_store,
        overlay_enabled: initial.overlay_enabled,
        attached: false,
        starting: true,
        tray_ok: true,
    };

    install_plugins(tauri::Builder::default())
        .manage::<LauncherState>(Mutex::new(ctx))
        .invoke_handler(tauri::generate_handler![
            ipc::get_status,
            ipc::get_toolkit_url,
            ipc::toggle_overlay,
            ipc::restart_backend,
            ipc::open_data_folder,
            ipc::open_dashboard_in_browser,
            ipc::open_external_url,
            ipc::quit,
            ipc::get_settings,
            ipc::save_settings,
        ])
        .setup(|app| {
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

            if let Err(e) = tray::setup_tray(&handle, "RL Toolkit") {
                eprintln!("[launcher] tray failed: {e}");
                if let Some(state) = handle.try_state::<LauncherState>() {
                    state.lock().unwrap().tray_ok = false;
                }
            }

            // Run probe → spawn off the main thread so the UI stays responsive.
            let app_for_probe = handle.clone();
            std::thread::spawn(move || {
                use crate::launcher::backend::{probe_status, spawn_sidecar, BackendOwnership, ProbeOutcome};
                let log_path = std::env::current_dir()
                    .unwrap_or_else(|_| std::path::PathBuf::from("."))
                    .join("data")
                    .join("launcher.log");
                let (plugins_dir, data_dir) = {
                    use tauri::Manager;
                    let state: tauri::State<LauncherState> = app_for_probe.state();
                    let ctx = state.lock().unwrap();
                    let s = ctx.settings.load();
                    (s.plugins_dir.clone(), s.data_dir.clone())
                };
                let url = format!("{}/api/status", toolkit_url.trim_end_matches('/'));
                let outcome = probe_status(&url, std::time::Duration::from_millis(500));

                let owned = match outcome {
                    ProbeOutcome::Toolkit => {
                        set_attached(&app_for_probe, true);
                        BackendOwnership::Attached
                    }
                    ProbeOutcome::Unreachable => {
                        match spawn_sidecar(&app_for_probe, log_path.clone(), plugins_dir, data_dir) {
                            Ok(b) => {
                                set_attached(&app_for_probe, false);
                                b
                            }
                            Err(e) => {
                                eprintln!("[launcher] spawn failed: {e}");
                                BackendOwnership::Unavailable
                            }
                        }
                    }
                    ProbeOutcome::Unrelated => {
                        eprintln!("[launcher] something else is on the toolkit port");
                        BackendOwnership::Unavailable
                    }
                };

                // Wait for ready (cap 10 s) when we just spawned.
                if matches!(owned, BackendOwnership::SpawnedSidecar(_) | BackendOwnership::SpawnedRaw(_)) {
                    let deadline = std::time::Instant::now() + std::time::Duration::from_secs(10);
                    while std::time::Instant::now() < deadline {
                        if probe_status(&url, std::time::Duration::from_millis(300)) == ProbeOutcome::Toolkit {
                            break;
                        }
                        std::thread::sleep(std::time::Duration::from_millis(200));
                    }
                }
                clear_starting(&app_for_probe);

                // Autostart the overlay if overlay_enabled was set.
                let auto = {
                    use tauri::Manager;
                    let state: tauri::State<LauncherState> = app_for_probe.state();
                    let ctx = state.lock().unwrap();
                    ctx.overlay_enabled
                };
                if auto {
                    if let Err(e) = crate::overlay_bridge::ensure_overlay(&app_for_probe) {
                        eprintln!("[launcher] overlay autostart failed: {e}");
                        // Persist enabled=false so we don't loop on next launch.
                        if let Some(s) = app_for_probe.try_state::<LauncherState>() {
                            let mut ctx = s.lock().unwrap();
                            ctx.overlay_enabled = false;
                            let mut on_disk = ctx.settings.load();
                            on_disk.overlay_enabled = false;
                            let _ = ctx.settings.save(&on_disk);
                        }
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
