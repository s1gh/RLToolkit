//! Build the launcher WebviewWindow. The dashboard is embedded as an
//! `<iframe>` inside launcher.html — Tauri 2's multi-webview-window
//! support has known issues on Linux/webkitgtk where set_position /
//! set_size silently no-op, so we use one webview + native HTML layout.

use tauri::{AppHandle, Theme, WebviewUrl, WebviewWindowBuilder};
#[cfg(not(any(
    target_os = "linux",
    target_os = "dragonfly",
    target_os = "freebsd",
    target_os = "netbsd",
    target_os = "openbsd",
)))]
use tauri::LogicalSize;

// Fraction of monitor logical size used as the minimum window size.
const MIN_W_FRACTION: f64 = 0.65;
const MIN_H_FRACTION: f64 = 0.70;
// Hard floor for headless / tiny-laptop cases.
const MIN_W_FLOOR: f64 = 800.0;
const MIN_H_FLOOR: f64 = 600.0;
// Hard ceiling so 4K+ monitors don't force a huge minimum that's
// annoying when docking the window next to the game.
const MIN_W_CEILING: f64 = 1600.0;
const MIN_H_CEILING: f64 = 1100.0;

/// Minimum inner size as a fraction of the monitor's logical size,
/// clamped to floor/ceiling. Falls back to floor when no monitor
/// info is available.
fn min_inner_size_for(monitor_size: Option<(f64, f64)>) -> (f64, f64) {
    let Some((mon_w, mon_h)) = monitor_size else {
        return (MIN_W_FLOOR, MIN_H_FLOOR);
    };
    let w = (mon_w * MIN_W_FRACTION).clamp(MIN_W_FLOOR, MIN_W_CEILING);
    let h = (mon_h * MIN_H_FRACTION).clamp(MIN_H_FLOOR, MIN_H_CEILING);
    (w, h)
}

fn primary_monitor_logical(app: &AppHandle) -> Option<(f64, f64)> {
    let monitor = app.primary_monitor().ok().flatten()?;
    let size = monitor.size();
    let scale = monitor.scale_factor();
    Some((size.width as f64 / scale, size.height as f64 / scale))
}

pub(crate) fn window_monitor_logical(win: &tauri::WebviewWindow) -> Option<(f64, f64)> {
    let monitor = win.current_monitor().ok().flatten()?;
    let size = monitor.size();
    let scale = monitor.scale_factor();
    Some((size.width as f64 / scale, size.height as f64 / scale))
}

pub fn build_launcher_window(
    app: &AppHandle,
    _toolkit_url: &str,
    initial: &crate::launcher::settings::LauncherSettings,
) -> tauri::Result<tauri::WebviewWindow> {
    // Pre-build estimate from the app-level primary monitor —
    // unreliable on Linux/Wayland (returns None during early setup),
    // so we re-derive after build.
    let initial_min = min_inner_size_for(primary_monitor_logical(app));
    let w = (initial.window_w.unwrap_or(720) as f64).max(initial_min.0);
    let h = (initial.window_h.unwrap_or(640) as f64).max(initial_min.1);

    let mut builder = WebviewWindowBuilder::new(
        app,
        "launcher",
        WebviewUrl::App("launcher.html".into()),
    )
    .title("RL Toolkit")
    .inner_size(w, h)
    .min_inner_size(initial_min.0, initial_min.1)
    .resizable(true)
    // Frameless: the launcher draws its own title bar (#strip in
    // launcher.html) so Linux/GNOME and Windows don't stack an OS
    // title bar above ours. Drag region is set in CSS; window
    // controls are HTML buttons calling __TAURI__.window.
    .decorations(false)
    // Theme drives prefers-color-scheme and on Windows controls the
    // DWM accent on the window shadow / resize border.
    .theme(Some(Theme::Dark))
    .visible(true);

    if let (Some(x), Some(y)) = (initial.window_x, initial.window_y) {
        builder = builder.position(x as f64, y as f64);
    }

    let win = builder.build()?;

    // Recompute the min using the live window's monitor — works on
    // Wayland where app.primary_monitor() returned None pre-build.
    let monitor = window_monitor_logical(&win).or_else(|| primary_monitor_logical(app));
    let (min_w, min_h) = min_inner_size_for(monitor);

    // Re-apply the min on the live GtkWindow after realize. The
    // builder's hint fires too early under webkitgtk and frequently
    // fails to propagate to xdg_toplevel.set_min_size / WM_NORMAL_HINTS.
    apply_linux_min_size(&win, min_w, min_h);

    // If the persisted size came from a smaller monitor, enlarge once
    // so the window opens at the right size.
    if w + 1.0 < min_w || h + 1.0 < min_h {
        enforce_min_size(&win, w.max(min_w), h.max(min_h));
    }

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

/// Linux: force the min-size at the GTK level. Two mechanisms because
/// neither is sufficient on every desktop: set_geometry_hints is the
/// protocol-correct path the compositor reads; set_size_request makes
/// the GTK widget refuse layout below the size even if the compositor
/// ignores the hint.
#[cfg(any(
    target_os = "linux",
    target_os = "dragonfly",
    target_os = "freebsd",
    target_os = "netbsd",
    target_os = "openbsd",
))]
fn apply_linux_min_size(win: &tauri::WebviewWindow, min_w: f64, min_h: f64) {
    use gtk::gdk::{Geometry, Gravity, WindowHints};
    use gtk::prelude::*;
    let Ok(gtk_window) = win.gtk_window() else {
        return;
    };
    let scale = win.scale_factor().unwrap_or(1.0);
    // GTK works in device pixels; multiply logical → physical so the
    // hint matches what tao's resize events report.
    let w_px = (min_w * scale).round() as i32;
    let h_px = (min_h * scale).round() as i32;
    // Only the min_* fields are read; WindowHints::MIN_SIZE tells the
    // WM to ignore the rest of the struct.
    let geom = Geometry::new(
        w_px, h_px,
        0, 0,
        0, 0,
        0, 0,
        0.0, 0.0,
        Gravity::NorthWest,
    );
    gtk_window.set_geometry_hints(
        None::<&gtk::Widget>,
        Some(&geom),
        WindowHints::MIN_SIZE,
    );
    gtk_window.set_size_request(w_px, h_px);
}

#[cfg(not(any(
    target_os = "linux",
    target_os = "dragonfly",
    target_os = "freebsd",
    target_os = "netbsd",
    target_os = "openbsd",
)))]
fn apply_linux_min_size(_win: &tauri::WebviewWindow, _min_w: f64, _min_h: f64) {}

/// Snap-back enforcement. On Linux drives the GTK widget directly
/// because WebviewWindow::set_size is unreliable under webkitgtk;
/// gtk_window.resize goes through gdk and the compositor honors it.
#[cfg(any(
    target_os = "linux",
    target_os = "dragonfly",
    target_os = "freebsd",
    target_os = "netbsd",
    target_os = "openbsd",
))]
fn enforce_min_size(win: &tauri::WebviewWindow, min_w: f64, min_h: f64) {
    use gtk::prelude::*;
    let Ok(gtk_window) = win.gtk_window() else {
        return;
    };
    let scale = win.scale_factor().unwrap_or(1.0);
    let w_px = (min_w * scale).round() as i32;
    let h_px = (min_h * scale).round() as i32;
    // Re-assert constraints — some Wayland compositors honor
    // set_min_size only at map time, not on subsequent reconfigures.
    apply_linux_min_size(win, min_w, min_h);
    // resize() drives the actual redraw; set_size_request alone won't
    // expand a user-shrunk window.
    gtk_window.resize(w_px, h_px);
}

#[cfg(not(any(
    target_os = "linux",
    target_os = "dragonfly",
    target_os = "freebsd",
    target_os = "netbsd",
    target_os = "openbsd",
)))]
fn enforce_min_size(win: &tauri::WebviewWindow, min_w: f64, min_h: f64) {
    let _ = win.set_size(LogicalSize::new(min_w, min_h));
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
