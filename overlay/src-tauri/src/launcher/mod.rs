pub mod backend;
pub mod ipc;
pub mod settings;
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
    };

    install_plugins(tauri::Builder::default())
        .manage::<LauncherState>(Mutex::new(ctx))
        .invoke_handler(tauri::generate_handler![
            ipc::get_status,
            ipc::toggle_overlay,
            ipc::restart_backend,
            ipc::open_data_folder,
            ipc::open_dashboard_in_browser,
            ipc::quit,
        ])
        .setup(move |app| {
            let url = {
                let state: tauri::State<LauncherState> = app.state();
                let url = state.lock().unwrap().toolkit_url.clone();
                url
            };
            window::build_launcher_window(&app.handle(), &url)?;
            Ok(())
        })
        .run(tauri::generate_context!())
        .expect("error while running rl-widget launcher");
}
