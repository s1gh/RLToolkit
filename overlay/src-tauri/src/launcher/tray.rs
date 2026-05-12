//! Launcher tray: Show / Toggle Overlay / Quit.

use tauri::menu::{Menu, MenuItem};
use tauri::tray::TrayIconBuilder;
use tauri::{AppHandle, Manager, Runtime};

pub fn setup_tray<R: Runtime>(app: &AppHandle<R>, tooltip: &str) -> Result<(), String> {
    let icon = app.default_window_icon().cloned()
        .ok_or_else(|| "no default window icon".to_string())?;

    let show = MenuItem::with_id(app, "show", "Show RL Toolkit", true, None::<&str>)
        .map_err(|e| format!("menu item: {e}"))?;
    let toggle = MenuItem::with_id(app, "toggle_overlay", "Toggle Overlay", true, None::<&str>)
        .map_err(|e| format!("menu item: {e}"))?;
    let edit = MenuItem::with_id(app, "overlay_edit_toggle", "Edit overlay layout", true, None::<&str>)
        .map_err(|e| format!("menu item: {e}"))?;
    let quit = MenuItem::with_id(app, "quit", "Quit", true, None::<&str>)
        .map_err(|e| format!("menu item: {e}"))?;

    let menu = Menu::with_items(app, &[&show, &toggle, &edit, &quit])
        .map_err(|e| format!("tray menu: {e}"))?;

    TrayIconBuilder::with_id("main")
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
                if let Some(w) = app.get_webview_window("main") {
                    let visible = w.is_visible().unwrap_or(false);
                    if visible { let _ = w.hide(); } else { let _ = w.show(); }
                }
            }
            "overlay_edit_toggle" => {
                if let Err(e) = crate::launcher::edit_mode::toggle(app) {
                    crate::log_warn!("[overlay-edit] toggle failed: {e}");
                }
            }
            "quit" => app.exit(0),
            _ => {}
        })
        .build(app)
        .map_err(|e| format!("build tray: {e}"))?;

    // Store the edit menu item so update_tray_label can flip its text
    // without needing a menu getter (which Tauri 2 doesn't expose on TrayIcon).
    if let Some(state) = app.try_state::<crate::launcher::edit_mode::EditModeState<R>>() {
        *state.tray_menu_item.lock().unwrap() = Some(edit);
    }

    Ok(())
}
