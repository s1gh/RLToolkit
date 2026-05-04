//! Build the launcher WebviewWindow + the embedded dashboard child webview.

use tauri::{AppHandle, LogicalPosition, LogicalSize, Manager, WebviewUrl, WebviewWindowBuilder};

const STRIP_H: f64 = 56.0;

pub fn build_launcher_window(
    app: &AppHandle,
    toolkit_url: &str,
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

    // Embed the dashboard as a sibling webview anchored below the strip.
    let parsed = url::Url::parse(toolkit_url)
        .map_err(|e| tauri::Error::WebviewLabelAlreadyExists(format!("bad toolkit url: {e}")))?;
    let size = win.inner_size()?;
    let scale = win.scale_factor()?;
    let logical_w = size.width as f64 / scale;
    let logical_h = size.height as f64 / scale - STRIP_H;

    // `add_child` lives on `Window`, not `WebviewWindow`; retrieve it via Manager.
    let window = win.get_window("launcher").ok_or_else(|| {
        tauri::Error::WebviewLabelAlreadyExists("launcher window not found".into())
    })?;
    window.add_child(
        tauri::webview::WebviewBuilder::new("dashboard", WebviewUrl::External(parsed))
            .auto_resize(),
        LogicalPosition::new(0.0, STRIP_H),
        LogicalSize::new(logical_w.max(1.0), logical_h.max(1.0)),
    )?;

    Ok(win)
}
