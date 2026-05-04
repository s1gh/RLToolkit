//! Build the launcher WebviewWindow. Loads launcher.html for the header
//! strip; the dashboard child webview is added as a sibling once the
//! backend is reachable (Phase C).

use tauri::{AppHandle, WebviewUrl, WebviewWindowBuilder};

pub fn build_launcher_window(app: &AppHandle) -> tauri::Result<tauri::WebviewWindow> {
    let win = WebviewWindowBuilder::new(
        app,
        "launcher",
        WebviewUrl::App("launcher.html".into()),
    )
    .title("RL Toolkit")
    .inner_size(720.0, 640.0)
    .min_inner_size(560.0, 480.0)
    .resizable(true)
    .decorations(true)
    .visible(true)
    .build()?;
    Ok(win)
}
