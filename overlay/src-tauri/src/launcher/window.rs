//! Build the launcher WebviewWindow. The dashboard is embedded as an
//! `<iframe>` inside `launcher.html` (see overlay/dist/launcher.{html,css,js}).
//! Tauri 2's multi-webview-window support has known issues on Linux/webkitgtk
//! where `set_position`/`set_size` on child webviews silently no-op, so we
//! use the iframe approach instead — one webview, native HTML layout.

use tauri::{AppHandle, WebviewUrl, WebviewWindowBuilder};
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
// Hard floor — applied if no monitor info is available (headless
// envs) or the monitor is smaller than expected. 800×600 is the
// universal "must fit on tiny laptop" baseline.
const MIN_W_FLOOR: f64 = 800.0;
const MIN_H_FLOOR: f64 = 600.0;
// Hard ceiling — on 4K+ monitors, 60% of the screen would force a
// huge minimum that's annoying when docking the window next to the
// game. Cap at a size that still fits the dashboard comfortably.
const MIN_W_CEILING: f64 = 1600.0;
const MIN_H_CEILING: f64 = 1100.0;

/// Compute the launcher's minimum inner size as a fraction of the
/// monitor's logical size, clamped to the floor/ceiling constants
/// above. Prefers the live window's current monitor (post-build,
/// reliable on all platforms) and falls back to the app's primary
/// monitor for the pre-build call. Final fallback is the floor.
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

fn window_monitor_logical(win: &tauri::WebviewWindow) -> Option<(f64, f64)> {
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
    // Pre-build estimate from the app-level primary monitor. This is
    // unreliable on Linux/Wayland where it returns None during early
    // setup (the GtkWidget hierarchy isn't realized yet), so we
    // re-derive after build using the window's current monitor.
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
    .decorations(true)
    .visible(true);

    if let (Some(x), Some(y)) = (initial.window_x, initial.window_y) {
        builder = builder.position(x as f64, y as f64);
    }

    let win = builder.build()?;

    // Recompute the min using the live window's monitor — this works
    // on Wayland where the pre-build app.primary_monitor() returns
    // None. The result is the authoritative minimum the resize
    // safety-net uses below; the builder hint above is a best-effort
    // first try.
    let monitor = window_monitor_logical(&win).or_else(|| primary_monitor_logical(app));
    let (min_w, min_h) = min_inner_size_for(monitor);

    // The builder's min_inner_size hint fires before the GtkWindow is
    // realized — under webkitgtk on Wayland the hint frequently fails
    // to propagate to xdg_toplevel.set_min_size, and under X11 the
    // WM_NORMAL_HINTS write can race with the initial map. Re-apply
    // directly on the live GtkWindow with the post-build min so the
    // hint lands after realize. Windows/macOS use the builder hint
    // and don't need this.
    apply_linux_min_size(&win, min_w, min_h);

    // If the persisted size from settings was below the post-build
    // min (e.g. settings persisted on a smaller monitor), enlarge the
    // window once now so it opens at the right size.
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

/// Linux: force the minimum size constraint at the GTK level so the
/// WM/compositor receives the right xdg_toplevel.set_min_size /
/// WM_NORMAL_HINTS even when tao's builder-time hint doesn't
/// propagate. Combines two mechanisms because neither is sufficient
/// alone on every desktop:
///   1. set_geometry_hints — the protocol-correct path; what the
///      compositor actually reads to constrain user resize.
///   2. set_size_request   — forces the GTK widget to refuse layout
///      below this size, so even if the compositor ignores the hint
///      the window contents won't reflow undersized.
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
    // GTK works in device pixels here (it's already gtk-scale-aware);
    // multiply logical → physical so the hint matches what tao's
    // resize events report.
    let w_px = (min_w * scale).round() as i32;
    let h_px = (min_h * scale).round() as i32;
    // Only the min_* fields matter — WindowHints::MIN_SIZE tells the
    // WM/compositor to ignore everything else in the struct. The
    // remaining args are required by Geometry::new but unused.
    let geom = Geometry::new(
        w_px, h_px, // min
        0, 0,       // max (unused without MAX_SIZE)
        0, 0,       // base (unused without BASE_SIZE)
        0, 0,       // increment (unused without RESIZE_INC)
        0.0, 0.0,   // aspect (unused without ASPECT)
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

/// Snap-back enforcement. Used by the resize-event safety net. On
/// Linux drives the GTK widget directly because `WebviewWindow::set_size`
/// is unreliable under webkitgtk (the file's module doc-comment notes
/// the analogous issue with set_position); using `gtk_window.resize`
/// goes through gdk which the compositor honors. Other platforms get
/// the Tauri call which works fine.
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
    // Re-assert the size constraints in case the compositor dropped
    // them (some Wayland compositors honor set_min_size only at map
    // time, not on subsequent reconfigure cycles).
    apply_linux_min_size(win, min_w, min_h);
    // resize() is what actually drives the compositor to redraw at
    // the requested size; set_size_request alone won't expand a
    // user-shrunk window.
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
