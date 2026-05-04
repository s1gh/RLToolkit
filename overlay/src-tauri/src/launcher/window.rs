//! Build the launcher WebviewWindow. The dashboard is embedded as an
//! `<iframe>` inside `launcher.html` (see overlay/dist/launcher.{html,css,js}).
//! Tauri 2's multi-webview-window support has known issues on Linux/webkitgtk
//! where `set_position`/`set_size` on child webviews silently no-op, so we
//! use the iframe approach instead — one webview, native HTML layout.

use tauri::{AppHandle, WebviewUrl, WebviewWindowBuilder};

pub fn build_launcher_window(
    app: &AppHandle,
    _toolkit_url: &str,
    initial: &crate::launcher::settings::LauncherSettings,
) -> tauri::Result<tauri::WebviewWindow> {
    let w = initial.window_w.unwrap_or(720) as f64;
    let h = initial.window_h.unwrap_or(640) as f64;

    let mut builder = WebviewWindowBuilder::new(
        app,
        "launcher",
        WebviewUrl::App("launcher.html".into()),
    )
    .title("RL Toolkit")
    .inner_size(w, h)
    .min_inner_size(560.0, 480.0)
    .resizable(true)
    .decorations(true)
    .visible(true);

    if let (Some(x), Some(y)) = (initial.window_x, initial.window_y) {
        builder = builder.position(x as f64, y as f64);
    }

    let win = builder.build()?;

    let win_for_geom = win.clone();
    let app_for_geom = app.clone();
    win.on_window_event(move |event| {
        use tauri::WindowEvent;
        if matches!(event, WindowEvent::Resized(_) | WindowEvent::Moved(_)) {
            persist_geometry(&app_for_geom, &win_for_geom);
        }
    });

    Ok(win)
}

fn persist_geometry(app: &AppHandle, win: &tauri::WebviewWindow) {
    use crate::launcher::ipc::LauncherState;
    use tauri::Manager;

    let (Ok(pos), Ok(size)) = (win.outer_position(), win.outer_size()) else {
        return;
    };
    let Some(state) = app.try_state::<LauncherState>() else {
        return;
    };
    let ctx = state.lock().unwrap();
    let mut s = ctx.settings.load();
    s.window_x = Some(pos.x);
    s.window_y = Some(pos.y);
    s.window_w = Some(size.width as i32);
    s.window_h = Some(size.height as i32);
    let _ = ctx.settings.save(&s);
}
