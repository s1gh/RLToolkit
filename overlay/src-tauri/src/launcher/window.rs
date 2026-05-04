//! Build the launcher WebviewWindow + the embedded dashboard child webview.

use tauri::{AppHandle, LogicalPosition, LogicalSize, Manager, WebviewUrl, WebviewWindowBuilder};

pub const STRIP_H: f64 = 56.0;

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

    // Compute the initial dashboard rect: full width, height-minus-strip.
    let scale = win.scale_factor()?;
    let size = win.inner_size()?;
    let logical_w = (size.width as f64 / scale).max(1.0);
    let logical_h = ((size.height as f64 / scale) - STRIP_H).max(1.0);

    // `add_child` lives on `Window`, not `WebviewWindow`; retrieve it via Manager.
    let parent_window = win.get_window("launcher").ok_or_else(|| {
        tauri::Error::WebviewLabelAlreadyExists("launcher window not found".into())
    })?;

    let parsed = url::Url::parse(toolkit_url)
        .map_err(|e| tauri::Error::WebviewLabelAlreadyExists(format!("bad toolkit url: {e}")))?;

    // Build the child without auto_resize — auto_resize tracks parent size 1:1,
    // which would put the dashboard *behind* the strip. We resize manually below.
    parent_window.add_child(
        tauri::webview::WebviewBuilder::new("dashboard", WebviewUrl::External(parsed)),
        LogicalPosition::new(0.0, STRIP_H),
        LogicalSize::new(logical_w, logical_h),
    )?;

    // Track parent resize events and reshape the child accordingly.
    let app_handle = app.clone();
    win.on_window_event(move |event| {
        if let tauri::WindowEvent::Resized(_) = event {
            reflow_dashboard(&app_handle);
        }
    });

    Ok(win)
}

/// Re-size and re-position the dashboard child webview so it fills the
/// area below the header strip. Called on parent resize and on initial
/// show. No-op if either webview is missing (during teardown).
pub fn reflow_dashboard(app: &AppHandle) {
    let Some(parent) = app.get_webview_window("launcher") else { return };
    let Ok(scale) = parent.scale_factor() else { return };
    let Ok(size) = parent.inner_size() else { return };
    let logical_w = (size.width as f64 / scale).max(1.0);
    let logical_h = ((size.height as f64 / scale) - STRIP_H).max(1.0);

    let Some(dashboard) = app.get_webview("dashboard") else { return };
    let _ = dashboard.set_position(LogicalPosition::new(0.0, STRIP_H));
    let _ = dashboard.set_size(LogicalSize::new(logical_w, logical_h));
}
