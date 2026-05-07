//! Launcher tray: Show / Toggle Overlay / Quit.

use tauri::menu::{Menu, MenuItem};
use tauri::tray::TrayIconBuilder;
use tauri::{AppHandle, Manager};

pub fn setup_tray(app: &AppHandle, tooltip: &str) -> Result<(), String> {
    let icon = app.default_window_icon().cloned()
        .ok_or_else(|| "no default window icon".to_string())?;

    let show = MenuItem::with_id(app, "show", "Show RL Toolkit", true, None::<&str>)
        .map_err(|e| format!("menu item: {e}"))?;
    let toggle = MenuItem::with_id(app, "toggle_overlay", "Toggle Overlay", true, None::<&str>)
        .map_err(|e| format!("menu item: {e}"))?;
    let quit = MenuItem::with_id(app, "quit", "Quit", true, None::<&str>)
        .map_err(|e| format!("menu item: {e}"))?;

    let menu = Menu::with_items(app, &[&show, &toggle, &quit])
        .map_err(|e| format!("tray menu: {e}"))?;

    TrayIconBuilder::new()
        .icon(icon)
        .tooltip(tooltip)
        .menu(&menu)
        .show_menu_on_left_click(true)
        .on_menu_event(|app, event| match event.id.as_ref() {
            "show" => {
                if let Some(w) = app.get_webview_window("launcher") {
                    let _ = w.unminimize();
                    let _ = w.show();
                    let _ = w.set_focus();
                }
            }
            "toggle_overlay" => {
                // Overlay window is built once during launcher setup; just
                // toggle its visibility. See toggle_overlay in ipc.rs for
                // why we never build webview windows from non-setup paths.
                if let Some(w) = app.get_webview_window("main") {
                    let visible = w.is_visible().unwrap_or(false);
                    if visible { let _ = w.hide(); } else { let _ = w.show(); }
                }
            }
            "quit" => app.exit(0),
            _ => {}
        })
        .build(app)
        .map_err(|e| format!("build tray: {e}"))?;
    Ok(())
}
