//! Build the launcher WebviewWindow. The dashboard is embedded as an
//! `<iframe>` inside `launcher.html` (see overlay/dist/launcher.{html,css,js}).
//! Tauri 2's multi-webview-window support has known issues on Linux/webkitgtk
//! where `set_position`/`set_size` on child webviews silently no-op, so we
//! use the iframe approach instead — one webview, native HTML layout.

use tauri::{AppHandle, WebviewUrl, WebviewWindowBuilder};

pub fn build_launcher_window(
    app: &AppHandle,
    _toolkit_url: &str,
) -> tauri::Result<tauri::WebviewWindow> {
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

    #[cfg(debug_assertions)]
    win.open_devtools();

    Ok(win)
}
