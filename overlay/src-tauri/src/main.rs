// rl-widget — overlay widget for RL Toolkit.
//
// Two modes: unified (no flag — one fullscreen window loading the
// toolkit's /overlay aggregator, which positions all enabled plugins)
// and per-plugin (--plugin=<name> — one window per plugin, sized and
// anchored from the plugin's manifest). In both cases the window is
// transparent, frameless, click-through, and pinned above all others.
//
// Because the window is click-through + skip-taskbar + undecorated +
// unfocused, it cannot be closed by clicking, alt-tabbing, or any
// in-window UI. Two exit paths are wired at startup:
//   - global hotkey (default Ctrl+Shift+Q, --quit-hotkey to override)
//     — REQUIRED. If registration fails (collision with another app),
//     startup aborts; the user picks a different combo.
//   - tray icon with a Quit menu item — best-effort. Logged-and-ignored
//     if the desktop environment can't host one (rare).
//
// Per-platform overlay primitive:
//   Linux/Wayland: wlr-layer-shell (overlay layer + anchored margins). The
//     compositor handles placement; no windowrule config required.
//     Click-through is NOT free here — layer-shell only handles where the
//     surface lives; pointer input is still claimed by the GTK window
//     unless we install an empty input shape (cairo region) on top of it.
//   Windows / X11 / macOS: regular always-on-top frameless window with
//     ignore_cursor_events=true. Tauri handles WS_EX_TRANSPARENT,
//     NSWindow.level, and X11 _NET_WM_STATE_ABOVE per platform.
//
//   Windows 11 also paints a hairline frame around the window via DWM
//   that survives decorations(false). It's compositor chrome (verified:
//   Discord's window-share doesn't see it, full-screen-share does), so
//   the suppression has to happen at the OS level. apply_windows_no_border
//   turns off DWMWA_BORDER_COLOR, the rounded-corner pass, non-client
//   rendering, and strips WS_THICKFRAME — see that function for the
//   per-knob rationale.
//
// Plugins can also reshape their own widget at runtime via Tauri commands
// (RLT.widget.* in the SDK) — see the `widget_*` handlers below.
//
// CLI:
//   rl-widget [--plugin=<name>] [--toolkit=URL] [--quit-hotkey=COMBO]

#![cfg_attr(
    all(not(debug_assertions), target_os = "windows"),
    windows_subsystem = "windows"
)]

use rl_widget::cli::Args;
use rl_widget::focus_watcher;
use rl_widget::launcher;

use clap::Parser;
use serde::Deserialize;
use std::sync::Mutex;
use tauri::menu::{Menu, MenuItem};
use tauri::tray::TrayIconBuilder;
use tauri::AppHandle;
#[cfg(not(target_os = "linux"))]
use tauri::{LogicalPosition, Manager};
use tauri::{LogicalSize, WebviewUrl, WebviewWindowBuilder};
use tauri_plugin_global_shortcut::{GlobalShortcutExt, Shortcut};

#[derive(Deserialize, Debug, Clone, Default)]
struct OverlayCfg {
    #[serde(default)]
    file: String,
    #[serde(default)]
    width: u32,
    #[serde(default)]
    height: u32,
    /// One of: top-left, top-right, bottom-left, bottom-right.
    #[serde(default)]
    anchor: String,
    #[serde(default)]
    offset_x: i32,
    #[serde(default)]
    offset_y: i32,
}

#[derive(Deserialize, Debug, Clone)]
struct PluginManifest {
    name: String,
    #[serde(default)]
    overlay: OverlayCfg,
}

/// Which mode the widget is running in.
///
/// `Plugin` carries the resolved manifest so the window builder can
/// size/anchor the surface from it without re-fetching. `Unified` carries
/// no extra data — the toolkit's /overlay page handles per-plugin layout.
#[derive(Debug, Clone)]
enum Mode {
    Plugin { manifest: OverlayCfg },
    Unified,
}

/// Live widget state — what the plugin has reshaped via RLT.widget.*.
/// Held behind a Mutex so the Tauri command handlers can mutate it from
/// any thread. The state seeds from the manifest and drifts as the plugin
/// calls in.
#[derive(Debug, Clone)]
struct WidgetState {
    width: i32,
    height: i32,
    anchor: String,
    margin_x: i32,
    margin_y: i32,
}

impl WidgetState {
    fn from_manifest(cfg: &OverlayCfg) -> Self {
        Self {
            width: cfg.width as i32,
            height: cfg.height as i32,
            anchor: cfg.anchor.clone(),
            margin_x: cfg.offset_x,
            margin_y: cfg.offset_y,
        }
    }
}

/// Per-plugin mode startup: fetch /api/plugins and return the entry's
/// overlay block. Returns Err on network failure, JSON parse failure, or
/// when the plugin isn't in the catalog. Treated as fatal by main() —
/// see the comment on probe_toolkit for why we don't open a window when
/// the toolkit can't be reached.
fn fetch_manifest(toolkit: &str, plugin: &str) -> Result<OverlayCfg, String> {
    let url = format!("{}/api/plugins", toolkit.trim_end_matches('/'));
    let resp = ureq::get(&url)
        .timeout(std::time::Duration::from_secs(2))
        .call()
        .map_err(|e| format!("manifest fetch failed: {e}"))?;
    let list: Vec<PluginManifest> = resp
        .into_json()
        .map_err(|e| format!("manifest parse failed: {e}"))?;
    list.into_iter()
        .find(|p| p.name == plugin)
        .map(|p| p.overlay)
        .ok_or_else(|| {
            format!(
                "plugin {plugin:?} not in {}/api/plugins",
                toolkit.trim_end_matches('/')
            )
        })
}

fn plugin_url(toolkit: &str, plugin: &str, file: &str, anchor: &str) -> String {
    let f = if file.is_empty() {
        "overlay.html"
    } else {
        file
    };
    // The view discriminator now lives on the <script data-view="overlay">
    // tag inside the file; the URL only carries parameterized runtime
    // info the SDK can't derive on its own (anchor for body alignment).
    format!(
        "{}/plugins/{}/{}?anchor={}",
        toolkit.trim_end_matches('/'),
        plugin,
        f,
        urlencoding_minimal(anchor),
    )
}

fn urlencoding_minimal(s: &str) -> String {
    s.replace(' ', "%20")
}

/// URL of the toolkit's aggregator overlay page (renders all enabled
/// plugins in one viewport). Used in unified mode.
fn unified_url(toolkit: &str) -> String {
    format!("{}/overlay", toolkit.trim_end_matches('/'))
}

/// Liveness check used by unified mode. Hits /api/status with a 2-second
/// timeout. Returns Err on failure — main() treats this as fatal and
/// refuses to open the window. We can't gracefully recover: the unified
/// window is fullscreen, click-through, undecorated, and skip-taskbar.
/// If we open it on a webview-error page (e.g. ERR_CONNECTION_REFUSED),
/// the user can't click on or focus anything underneath it and has no
/// path to close it short of Task Manager. Better to fail loudly at
/// startup than to lock up the desktop.
fn probe_toolkit(toolkit: &str) -> Result<(), String> {
    let base = toolkit.trim_end_matches('/');
    let url = format!("{}/api/status", base);
    ureq::get(&url)
        .timeout(std::time::Duration::from_secs(2))
        .call()
        .map(|_| ())
        .map_err(|e| format!("toolkit unreachable at {}: {}", base, e))
}

/// Best-effort POST of the bound monitor's logical (w, h) to
/// /api/overlay/surface/detected. The dashboard setting is the canonical
/// source of truth; this is purely informational so the editor and the
/// dashboard "Use detected" button have a sane default. Failure is
/// logged but never propagated — the launcher must keep starting even
/// when the toolkit is offline at this exact instant.
fn report_detected_surface(toolkit: &str, w: f64, h: f64) {
    let base = toolkit.trim_end_matches('/');
    let url = format!("{}/api/overlay/surface/detected", base);
    let body = format!(
        r#"{{"width":{},"height":{}}}"#,
        w.round() as i64,
        h.round() as i64
    );
    let result = ureq::post(&url)
        .timeout(std::time::Duration::from_secs(2))
        .set("Content-Type", "application/json")
        .send_string(&body);
    if let Err(e) = result {
        eprintln!("[rl-widget] detected-surface report failed: {e}");
    }
}

// ─── Tauri commands (the RLT.widget.* surface) ──────────────────
//
// Each command applies the change to BOTH:
//   1. Tauri's window (so the change works on Windows/macOS too)
//   2. On Linux, the layer-shell surface (which has its own anchor/margin
//      protocol that ignores xdg-toplevel positioning)
// and updates the shared WidgetState so re-anchoring / re-margining can
// reuse the previously-set values.
//   3. Gates on Mode — no-ops with a log line in unified mode (where the
//      window is shared across all plugins and per-plugin reshape is wrong).

/// Returns true when the widget is in unified mode. Each command
/// handler calls this to decide whether to run or no-op + log.
///
/// `tauri::State<'_, Mode>` derefs to `&Mode`, so a single `*` peels
/// it back into a `Mode` we can pattern-match on.
fn ignored_in_unified(mode: &tauri::State<'_, Mode>, fn_name: &str) -> bool {
    if matches!(**mode, Mode::Unified) {
        eprintln!("[rl-widget] {} ignored in unified mode", fn_name);
        return true;
    }
    false
}

#[tauri::command]
fn widget_size(
    width: u32,
    height: u32,
    window: tauri::WebviewWindow,
    state: tauri::State<'_, Mutex<WidgetState>>,
    mode: tauri::State<'_, Mode>,
) -> Result<(), String> {
    if ignored_in_unified(&mode, "widget_size") {
        return Ok(());
    }
    let _ = window.set_size(LogicalSize::new(width as f64, height as f64));
    if let Ok(mut s) = state.lock() {
        s.width = width as i32;
        s.height = height as i32;
    }
    #[cfg(target_os = "linux")]
    apply_linux_size(&window, width as i32, height as i32);
    Ok(())
}

#[tauri::command]
fn widget_anchor(
    anchor: String,
    window: tauri::WebviewWindow,
    state: tauri::State<'_, Mutex<WidgetState>>,
    mode: tauri::State<'_, Mode>,
) -> Result<(), String> {
    if ignored_in_unified(&mode, "widget_anchor") {
        return Ok(());
    }
    if let Ok(mut s) = state.lock() {
        s.anchor = anchor.clone();
    }
    #[cfg(target_os = "linux")]
    apply_linux_anchor(&window, &state);
    #[cfg(not(target_os = "linux"))]
    apply_pixel_position(&window, &state);
    Ok(())
}

#[tauri::command]
fn widget_margin(
    x: i32,
    y: i32,
    window: tauri::WebviewWindow,
    state: tauri::State<'_, Mutex<WidgetState>>,
    mode: tauri::State<'_, Mode>,
) -> Result<(), String> {
    if ignored_in_unified(&mode, "widget_margin") {
        return Ok(());
    }
    if let Ok(mut s) = state.lock() {
        s.margin_x = x;
        s.margin_y = y;
    }
    #[cfg(target_os = "linux")]
    apply_linux_anchor(&window, &state);
    #[cfg(not(target_os = "linux"))]
    apply_pixel_position(&window, &state);
    Ok(())
}

#[tauri::command]
fn widget_opacity(
    opacity: f64,
    window: tauri::WebviewWindow,
    mode: tauri::State<'_, Mode>,
) -> Result<(), String> {
    if ignored_in_unified(&mode, "widget_opacity") {
        return Ok(());
    }
    // Tauri exposes opacity on each platform's underlying window. On Linux
    // it's gtk_window.set_opacity; on Windows it's WS_EX_LAYERED alpha; on
    // macOS it's NSWindow.alphaValue. All clamp 0..=1.
    let clamped = opacity.clamp(0.0, 1.0);
    #[cfg(target_os = "linux")]
    if let Ok(gtk_window) = window.gtk_window() {
        use gtk::prelude::*;
        gtk_window.set_opacity(clamped);
    }
    #[cfg(not(target_os = "linux"))]
    {
        // Tauri's set_opacity is gated behind a feature; fall back to the
        // platform's window if/when that lands. For the spike we no-op so
        // the API surface stays uniform.
        let _ = (clamped, &window);
    }
    Ok(())
}

#[tauri::command]
fn widget_visible(
    visible: bool,
    window: tauri::WebviewWindow,
    mode: tauri::State<'_, Mode>,
) -> Result<(), String> {
    if ignored_in_unified(&mode, "widget_visible") {
        return Ok(());
    }
    if visible {
        let _ = window.show();
    } else {
        let _ = window.hide();
    }
    Ok(())
}

// ─── Platform helpers ───────────────────────────────────────────

#[cfg(target_os = "linux")]
fn apply_linux_size(window: &tauri::WebviewWindow, w: i32, h: i32) {
    use gtk::prelude::*;
    if let Ok(gtk_window) = window.gtk_window() {
        gtk_window.set_size_request(w, h);
        gtk_window.resize(w, h);
    }
}

#[cfg(target_os = "linux")]
fn apply_linux_anchor(window: &tauri::WebviewWindow, state: &tauri::State<'_, Mutex<WidgetState>>) {
    use gtk_layer_shell::LayerShell;
    let snapshot = match state.lock() {
        Ok(s) => s.clone(),
        Err(_) => return,
    };
    let Ok(gtk_window) = window.gtk_window() else {
        return;
    };

    // Reset anchors first — gtk-layer-shell remembers prior set_anchor calls,
    // so flipping from bottom-left to top-right needs the old edges cleared.
    use gtk_layer_shell::Edge;
    for e in [Edge::Top, Edge::Bottom, Edge::Left, Edge::Right] {
        gtk_window.set_anchor(e, false);
    }
    let (vert, horiz) = parse_anchor(&snapshot.anchor);
    gtk_window.set_anchor(vert, true);
    gtk_window.set_anchor(horiz, true);
    gtk_window.set_layer_shell_margin(vert, snapshot.margin_y);
    gtk_window.set_layer_shell_margin(horiz, snapshot.margin_x);
}

/// Windows / macOS / X11 path: layer-shell isn't available, so we resolve
/// the anchor + margin to absolute pixel coordinates against the window's
/// current monitor and call set_position. Recomputes on every change.
#[cfg(not(target_os = "linux"))]
fn apply_pixel_position(
    window: &tauri::WebviewWindow,
    state: &tauri::State<'_, Mutex<WidgetState>>,
) {
    let snapshot = match state.lock() {
        Ok(s) => s.clone(),
        Err(_) => return,
    };
    let Ok(Some(monitor)) = window.current_monitor() else {
        return;
    };
    let mon_size = monitor.size();
    let scale = monitor.scale_factor();
    let mon_w = mon_size.width as f64 / scale;
    let mon_h = mon_size.height as f64 / scale;

    let anchor = snapshot.anchor.to_ascii_lowercase();
    let x = if anchor.contains("right") {
        mon_w - snapshot.width as f64 - snapshot.margin_x as f64
    } else {
        snapshot.margin_x as f64
    };
    let y = if anchor.contains("bottom") {
        mon_h - snapshot.height as f64 - snapshot.margin_y as f64
    } else {
        snapshot.margin_y as f64
    };
    let _ = window.set_position(LogicalPosition::new(x, y));
}

/// Unified-mode fullscreen pass on non-Linux platforms. Sets the window
/// size to the current monitor's logical size and positions at (0, 0).
/// The toolkit's /overlay page handles per-plugin positioning inside.
#[cfg(not(target_os = "linux"))]
fn apply_fullscreen_position(window: &tauri::WebviewWindow, toolkit: &str) {
    let Ok(Some(monitor)) = window.current_monitor() else {
        return;
    };
    let mon_size = monitor.size();
    let scale = monitor.scale_factor();
    let mon_w = mon_size.width as f64 / scale;
    let mon_h = mon_size.height as f64 / scale;
    report_detected_surface(toolkit, mon_w, mon_h);
    let _ = window.set_size(LogicalSize::new(mon_w, mon_h));
    let _ = window.set_position(LogicalPosition::new(0.0, 0.0));
}

/// Suppress the Windows 11 hairline frame around the overlay window.
///
/// The artifact is compositor-painted: Discord's per-window screen
/// share doesn't capture it, but full-screen share does. So we have to
/// turn it off at the DWM / window-style level — HTML/CSS can't reach
/// it, and `decorations(false)` doesn't either.
///
/// Four independent knobs, all best-effort. Each is harmless on
/// Windows builds that don't recognize it (the API just returns a
/// non-zero HRESULT we ignore) or where the bit isn't set:
///
///   1. DWMWA_BORDER_COLOR = DWMWA_COLOR_NONE — explicit "no border
///      color" on the window. Win11 22000+. May not be enough on its
///      own (the prior attempt at this didn't fix the bug) but it's
///      free to set.
///   2. DWMWA_WINDOW_CORNER_PREFERENCE = DWMWCP_DONOTROUND — opt out
///      of the Win11 rounded-corner pass, which can paint a frame as
///      part of the corner treatment.
///   3. DWMWA_NCRENDERING_POLICY = DWMNCRP_DISABLED — disable
///      non-client rendering on this HWND. Kills DWM-drawn frame
///      chrome including the drop-shadow edge.
///   4. Strip WS_THICKFRAME and re-apply with SWP_FRAMECHANGED.
///      Tauri's decorations(false) clears WS_CAPTION but typically
///      leaves the sizing border so the window stays programmatically
///      resizable. We don't need that — `resizable(false)` is set —
///      and the 1px sizing frame is a likely outline source.
#[cfg(target_os = "windows")]
fn apply_windows_no_border(window: &tauri::WebviewWindow) {
    use windows_sys::Win32::Foundation::HWND;
    use windows_sys::Win32::Graphics::Dwm::{
        DwmSetWindowAttribute, DWMNCRP_DISABLED, DWMWA_BORDER_COLOR, DWMWA_COLOR_NONE,
        DWMWA_NCRENDERING_POLICY, DWMWA_WINDOW_CORNER_PREFERENCE, DWMWCP_DONOTROUND,
    };
    use windows_sys::Win32::UI::WindowsAndMessaging::{
        GetWindowLongPtrW, SetWindowLongPtrW, SetWindowPos, GWL_STYLE, SWP_FRAMECHANGED,
        SWP_NOACTIVATE, SWP_NOMOVE, SWP_NOSIZE, SWP_NOZORDER, WS_THICKFRAME,
    };

    let Ok(hwnd) = window.hwnd() else { return };
    // Tauri returns windows::Win32::Foundation::HWND (typed `windows`
    // crate, a tuple struct around *mut c_void). windows-sys aliases
    // HWND to that same raw pointer, so the inner field is the value
    // we pass to all the Win32 calls below.
    let hwnd: HWND = hwnd.0;

    // (1) DWMWA_BORDER_COLOR = DWMWA_COLOR_NONE.
    // Safety: hwnd is a valid window handle owned by Tauri for the
    // lifetime of this window. DwmSetWindowAttribute reads
    // `cbAttribute` bytes synchronously and returns. sizeof(COLORREF)
    // = sizeof(u32) = 4.
    unsafe {
        let color: u32 = DWMWA_COLOR_NONE;
        let _ = DwmSetWindowAttribute(
            hwnd,
            DWMWA_BORDER_COLOR as u32,
            &color as *const u32 as *const _,
            std::mem::size_of::<u32>() as u32,
        );
    }

    // (2) DWMWA_WINDOW_CORNER_PREFERENCE = DWMWCP_DONOTROUND.
    // Safety: same as above; the attribute is a 32-bit enum value.
    unsafe {
        let pref: u32 = DWMWCP_DONOTROUND as u32;
        let _ = DwmSetWindowAttribute(
            hwnd,
            DWMWA_WINDOW_CORNER_PREFERENCE as u32,
            &pref as *const u32 as *const _,
            std::mem::size_of::<u32>() as u32,
        );
    }

    // (3) DWMWA_NCRENDERING_POLICY = DWMNCRP_DISABLED.
    // Safety: same as above; the attribute is a 32-bit enum value.
    unsafe {
        let policy: u32 = DWMNCRP_DISABLED as u32;
        let _ = DwmSetWindowAttribute(
            hwnd,
            DWMWA_NCRENDERING_POLICY as u32,
            &policy as *const u32 as *const _,
            std::mem::size_of::<u32>() as u32,
        );
    }

    // (4) Strip WS_THICKFRAME and notify the frame changed.
    // Safety: hwnd is a valid HWND; GetWindowLongPtrW / SetWindowLongPtrW
    // take a pointer-sized integer style word and return the previous
    // value. SetWindowPos with SWP_FRAMECHANGED forces DWM to recompute
    // the non-client area against the new style. SWP_NOMOVE | NOSIZE |
    // NOZORDER | NOACTIVATE means we don't actually move/resize/restack.
    unsafe {
        let style = GetWindowLongPtrW(hwnd, GWL_STYLE);
        if style != 0 {
            let new_style = style & !(WS_THICKFRAME as isize);
            if new_style != style {
                SetWindowLongPtrW(hwnd, GWL_STYLE, new_style);
                let _ = SetWindowPos(
                    hwnd,
                    std::ptr::null_mut(),
                    0,
                    0,
                    0,
                    0,
                    SWP_FRAMECHANGED | SWP_NOMOVE | SWP_NOSIZE | SWP_NOZORDER | SWP_NOACTIVATE,
                );
            }
        }
    }
}

/// Build the tray icon with a Quit menu item. Best-effort: any error is
/// returned to the caller, which logs and continues. The hotkey is the
/// guaranteed exit path; the tray is a discoverability aid.
fn setup_tray(app: &AppHandle, tooltip: &str) -> Result<(), String> {
    let icon = app
        .default_window_icon()
        .cloned()
        .ok_or_else(|| "no default window icon".to_string())?;

    let quit = MenuItem::with_id(app, "quit", "Quit", true, None::<&str>)
        .map_err(|e| format!("build menu item: {e}"))?;
    let menu = Menu::with_items(app, &[&quit]).map_err(|e| format!("build tray menu: {e}"))?;

    TrayIconBuilder::new()
        .icon(icon)
        .tooltip(tooltip)
        .menu(&menu)
        .show_menu_on_left_click(true)
        .on_menu_event(|app, event| {
            if event.id.as_ref() == "quit" {
                app.exit(0);
            }
        })
        .build(app)
        .map_err(|e| format!("build tray icon: {e}"))?;
    Ok(())
}

/// Register the global quit hotkey. REQUIRED — see the Args::quit_hotkey
/// doc comment for why. Returns Err on parse failure or if the OS reports
/// the combo is already taken; the caller turns that into a startup
/// abort with a stderr message telling the user to pick another combo.
fn setup_hotkey(app: &AppHandle, combo: &str) -> Result<(), String> {
    let shortcut: Shortcut = combo
        .parse()
        .map_err(|e| format!("could not parse quit hotkey {combo:?}: {e}"))?;

    let app_for_handler = app.clone();
    let shortcut_for_handler = shortcut;

    app.global_shortcut()
        .on_shortcut(shortcut, move |_app, sc, _event| {
            if *sc == shortcut_for_handler {
                app_for_handler.exit(0);
            }
        })
        .map_err(|e| format!("could not register quit hotkey {combo:?}: {e}"))?;
    Ok(())
}

fn launcher_mode_active(args: &Args) -> bool {
    if args.no_launcher {
        return false;
    }
    if args.launcher {
        return true;
    }
    // Default: launcher when no overlay-shaping flags were passed.
    args.plugin.is_none()
}

/// Build the overlay window from the launcher's app instance.
/// Uses unified mode with hardcoded defaults — the launcher owns the
/// lifecycle, so we skip hotkey and tray registration here (C2 will add
/// those). Focus-watcher is also skipped: in launcher mode the overlay
/// is always shown when toggled on, regardless of game window.
fn build_overlay_for_launcher(app: &AppHandle) -> Result<(), String> {
    use tauri::Manager;
    let app = app.clone();

    // GTK window creation must happen on the main thread. The launcher
    // calls us from a background probe thread, so dispatch to main.
    app.clone()
        .run_on_main_thread(move || {
            // If the window already exists (e.g. toggle called twice), just show it.
            if let Some(w) = app.get_webview_window("main") {
                let _ = w.show();
                return;
            }

            let toolkit_url =
                if let Some(state) = app.try_state::<crate::launcher::ipc::LauncherState>() {
                    state.lock().unwrap().toolkit_url.clone()
                } else {
                    "http://localhost:49200".to_string()
                };

            let url = unified_url(&toolkit_url);
            let title = "RL Toolkit – Overlay".to_string();

            if let Err(e) = build_overlay_window(
                &app,
                &Mode::Unified,
                &url,
                &title,
                false,
                None,
                &toolkit_url,
            ) {
                eprintln!("[launcher] build_overlay_window failed: {e}");
                return;
            }

            // The focus watcher (spawned in launcher::run) drives real
            // __rlt_focus__ messages into the overlay webview. Plugins with
            // hide_when_unfocused=1 wait on those before rendering.
        })
        .map_err(|e| e.to_string())
}

/// Construct the overlay webview window inside the given app.
///
/// Handles both the overlay-only path (called from `main_overlay`'s
/// `.setup` closure) and the launcher path (called from
/// `build_overlay_for_launcher`). The window is always labelled `"main"`.
///
/// Parameters:
/// - `mode`         — Plugin or Unified, controls sizing/anchoring.
/// - `url`          — Webview URL string (already constructed by caller).
/// - `title`        — Window title string.
/// - `persist_cache`— When false (default), opens in incognito mode so
///                    the webview cache is discarded on each launch.
/// - `monitor`      — Optional output index (Linux/Wayland only).
fn build_overlay_window(
    app: &AppHandle,
    mode: &Mode,
    url: &str,
    title: &str,
    persist_cache: bool,
    monitor: Option<usize>,
    toolkit: &str,
) -> tauri::Result<()> {
    let parsed = url::Url::parse(url).map_err(tauri::Error::InvalidUrl)?;

    let mut builder = WebviewWindowBuilder::new(app, "main", WebviewUrl::External(parsed))
        .title(title)
        .decorations(false)
        .transparent(true)
        .always_on_top(true)
        .skip_taskbar(true)
        .resizable(false)
        .focused(false)
        .visible(false);

    if !persist_cache {
        builder = builder.incognito(true);
    }

    if let Mode::Plugin { manifest } = mode {
        builder = builder.inner_size(manifest.width as f64, manifest.height as f64);
    }

    let window = builder.build()?;

    #[cfg(not(target_os = "linux"))]
    {
        let _ = window.set_ignore_cursor_events(true);
    }

    #[cfg(target_os = "windows")]
    apply_windows_no_border(&window);

    match mode {
        Mode::Plugin { manifest } => {
            #[cfg(target_os = "linux")]
            apply_layer_shell_plugin(&window, manifest, monitor);

            #[cfg(not(target_os = "linux"))]
            {
                // For plugin mode from launcher, we don't have managed WidgetState.
                // apply_pixel_position requires a tauri::State, so we skip on non-linux
                // when called from the launcher path. The window will appear at default
                // position; the toolkit handles per-plugin layout.
                let _ = monitor;
            }
        }
        Mode::Unified => {
            #[cfg(target_os = "linux")]
            apply_layer_shell_unified(&window, monitor, toolkit);

            #[cfg(not(target_os = "linux"))]
            apply_fullscreen_position(&window, toolkit);
        }
    }

    window.show()?;

    Ok(())
}

fn main() {
    let args = Args::parse();

    if launcher_mode_active(&args) {
        // Install the overlay factory the launcher can call later.
        // Must be a bare fn pointer (no captures), so the body calls a
        // free function that takes only &AppHandle.
        rl_widget::overlay_bridge::install(|app| build_overlay_for_launcher(app));
        launcher::run(args);
        return;
    }

    main_overlay(args);
}

fn main_overlay(args: Args) {
    let (mode, url, title) = match args.plugin.clone() {
        Some(name) if !name.is_empty() => {
            let manifest = match fetch_manifest(&args.toolkit, &name) {
                Ok(m) => m,
                Err(e) => {
                    eprintln!("[rl-widget] {}", e);
                    eprintln!("[rl-widget] start the toolkit first (e.g. ./rl-toolkit) and retry");
                    std::process::exit(1);
                }
            };
            let url = plugin_url(&args.toolkit, &name, &manifest.file, &manifest.anchor);
            eprintln!("[rl-widget] plugin={} url={}", name, url);
            let title = format!("RL Toolkit – {}", name);
            (Mode::Plugin { manifest }, url, title)
        }
        _ => {
            if let Err(e) = probe_toolkit(&args.toolkit) {
                eprintln!("[rl-widget] {}", e);
                eprintln!("[rl-widget] start the toolkit first (e.g. ./rl-toolkit) and retry");
                std::process::exit(1);
            }
            let url = unified_url(&args.toolkit);
            eprintln!("[rl-widget] unified mode url={}", url);
            (Mode::Unified, url, "RL Toolkit – Overlay".to_string())
        }
    };

    // Seed WidgetState. In Plugin mode, from the manifest. In Unified
    // mode, a default — the value is never read because the widget_*
    // command handlers gate on Mode and early-return.
    let widget_state = match &mode {
        Mode::Plugin { manifest, .. } => WidgetState::from_manifest(manifest),
        Mode::Unified => WidgetState {
            width: 0,
            height: 0,
            anchor: String::new(),
            margin_x: 0,
            margin_y: 0,
        },
    };

    let mode_for_setup = mode.clone();
    let quit_hotkey = args.quit_hotkey.clone();
    let tray_tooltip = title.clone();
    let args_for_setup = args.clone();

    tauri::Builder::default()
        .plugin(tauri_plugin_global_shortcut::Builder::new().build())
        .manage(Mutex::new(widget_state))
        .manage(mode.clone())
        .invoke_handler(tauri::generate_handler![
            widget_size,
            widget_anchor,
            widget_margin,
            widget_opacity,
            widget_visible,
        ])
        .setup(move |app| {
            // Register the quit hotkey FIRST. If it can't be claimed,
            // bail before opening the window — without this combo the
            // user has no in-app exit path.
            if let Err(e) = setup_hotkey(app.handle(), &quit_hotkey) {
                eprintln!("[rl-widget] {}", e);
                eprintln!("[rl-widget] pick a different combo with --quit-hotkey=<COMBO>");
                std::process::exit(1);
            }
            eprintln!("[rl-widget] quit hotkey: {}", quit_hotkey);

            // Tray is best-effort. Some minimal Linux desktops can't
            // host one; the hotkey still works.
            if let Err(e) = setup_tray(app.handle(), &tray_tooltip) {
                eprintln!(
                    "[rl-widget] tray icon failed to register ({}); use {} to quit",
                    e, quit_hotkey
                );
            }

            // Build the overlay window via the shared helper.
            build_overlay_window(
                app.handle(),
                &mode_for_setup,
                &url,
                &title,
                args_for_setup.persist_cache,
                args_for_setup.monitor,
                &args_for_setup.toolkit,
            )?;

            // In plugin mode on non-linux, apply pixel positioning using
            // the managed WidgetState (only available in the overlay-only app).
            #[cfg(not(target_os = "linux"))]
            if let Mode::Plugin { .. } = &mode_for_setup {
                if let Some(window) = app.get_webview_window("main") {
                    let state = app.state::<Mutex<WidgetState>>();
                    apply_pixel_position(&window, &state);
                }
            }

            // Start the foreground-window watcher. It owns a clone of the
            // app handle and runs until the process exits. Disabled (early
            // return) when --game-match="".
            let rule = focus_watcher::match_rule_from_arg(args_for_setup.game_match.as_deref());
            focus_watcher::spawn(app.handle().clone(), rule);

            Ok(())
        })
        .run(tauri::generate_context!())
        .expect("error while running rl-widget");
}

// ─── Linux: wlr-layer-shell init ────────────────────────────────

/// Layer-shell setup shared by both modes. Makes the GTK surface
/// alpha-aware, binds the layer-shell protocol, parks on the overlay
/// layer, disables keyboard focus, and clears any exclusive zone.
/// Sizing and anchoring are mode-specific and applied separately.
#[cfg(target_os = "linux")]
fn init_layer_shell_common(
    window: &tauri::WebviewWindow,
    monitor_index: Option<usize>,
) -> Option<gtk::Window> {
    use gtk::prelude::*;
    use gtk_layer_shell::{Layer, LayerShell};

    let gtk_window = match window.gtk_window() {
        Ok(w) => w,
        Err(e) => {
            eprintln!("[rl-widget] gtk_window unavailable, skipping layer-shell: {e}");
            return None;
        }
    };

    if let Some(screen) = gtk::prelude::GtkWindowExt::screen(&gtk_window) {
        if let Some(visual) = screen.rgba_visual() {
            gtk_window.set_visual(Some(&visual));
        }
    }
    gtk_window.set_app_paintable(true);

    gtk_window.init_layer_shell();

    // Resolve which GDK monitor to bind the layer surface to. Without this
    // wlr-layer-shell fans the surface across every connected output
    // simultaneously. We bind to the primary monitor by default; --monitor=N
    // overrides for users whose game lives on a non-primary display.
    //
    // set_monitor() is a LayerShell trait method — it must be called AFTER
    // init_layer_shell(), which installs the LayerShell state on the window.
    let chosen_monitor = if let Some(display) = gtk::gdk::Display::default() {
        let mon = if let Some(n) = monitor_index {
            display.monitor(n as i32)
        } else {
            display.primary_monitor().or_else(|| display.monitor(0))
        };
        if mon.is_none() {
            eprintln!(
                "[rl-widget] could not resolve a GDK monitor (index={monitor_index:?}); \
                 layer surface will fan across all outputs"
            );
        }
        mon
    } else {
        eprintln!("[rl-widget] no default GDK display; layer surface will fan across all outputs");
        None
    };

    if let Some(ref monitor) = chosen_monitor {
        gtk_window.set_monitor(monitor);
        let label = monitor
            .model()
            .map(|m| m.to_string())
            .unwrap_or_else(|| format!("index {:?}", monitor_index.unwrap_or(0)));
        eprintln!("[rl-widget] layer-shell bound to monitor: {label:?}");
    }

    gtk_window.set_layer(Layer::Overlay);
    gtk_window.set_keyboard_interactivity(false);
    gtk_window.set_exclusive_zone(0);

    // Click-through. Without this the overlay still grabs every pointer
    // event even though it's transparent — clicks on the game underneath
    // never reach RL. An empty input shape tells GDK "this surface has
    // no interactive area," so the compositor routes input to whatever's
    // beneath.
    //
    // realize() must run first: input_shape_combine_region needs the
    // backing GdkWindow to exist, and on a not-yet-shown GTK window the
    // call would silently no-op.
    gtk_window.realize();
    let empty_region = gtk::cairo::Region::create();
    gtk_window.input_shape_combine_region(Some(&empty_region));

    Some(gtk_window.into())
}

/// Per-plugin mode: anchor two edges from the manifest, fixed size,
/// margins from the manifest's offset_x/offset_y.
#[cfg(target_os = "linux")]
fn apply_layer_shell_plugin(
    window: &tauri::WebviewWindow,
    cfg: &OverlayCfg,
    monitor_index: Option<usize>,
) {
    use gtk::prelude::*;
    use gtk_layer_shell::LayerShell;

    let Some(gtk_window) = init_layer_shell_common(window, monitor_index) else {
        return;
    };

    gtk_window.set_size_request(cfg.width as i32, cfg.height as i32);
    gtk_window.set_default_size(cfg.width as i32, cfg.height as i32);
    gtk_window.set_resizable(false);

    let (vert_edge, horiz_edge) = parse_anchor(&cfg.anchor);
    gtk_window.set_anchor(vert_edge, true);
    gtk_window.set_anchor(horiz_edge, true);
    gtk_window.set_layer_shell_margin(vert_edge, cfg.offset_y);
    gtk_window.set_layer_shell_margin(horiz_edge, cfg.offset_x);
}

/// Unified mode: anchor all four edges, all margins zero. Don't pin a
/// size — the compositor sizes the surface to the output, which is the
/// standard fullscreen-overlay layer-shell idiom.
#[cfg(target_os = "linux")]
fn apply_layer_shell_unified(
    window: &tauri::WebviewWindow,
    monitor_index: Option<usize>,
    toolkit: &str,
) {
    use gtk::prelude::*;
    use gtk_layer_shell::{Edge, LayerShell};

    // Resolve the chosen GDK monitor and report its logical size to the
    // toolkit. Same selection logic as init_layer_shell_common (primary
    // when no index given). Logical = device pixels / scale factor.
    let chosen_size = if let Some(display) = gtk::gdk::Display::default() {
        let mon = if let Some(n) = monitor_index {
            display.monitor(n as i32)
        } else {
            display.primary_monitor().or_else(|| display.monitor(0))
        };
        mon.map(|m| {
            let geo = m.geometry();
            let scale = m.scale_factor().max(1) as f64;
            let w = geo.width() as f64 / scale;
            let h = geo.height() as f64 / scale;
            (w, h)
        })
    } else {
        None
    };

    if let Some((w, h)) = chosen_size {
        report_detected_surface(toolkit, w, h);
    }

    let Some(gtk_window) = init_layer_shell_common(window, monitor_index) else {
        return;
    };

    for e in [Edge::Top, Edge::Bottom, Edge::Left, Edge::Right] {
        gtk_window.set_anchor(e, true);
        gtk_window.set_layer_shell_margin(e, 0);
    }

    // Pin the GTK window's default size to the monitor's logical size.
    // Without this, WebKitGTK initially lays out the page at Tauri's
    // default 800×600, and even though layer-shell anchoring stretches
    // the surface, the webview's CSS viewport (100vh) sometimes stays
    // at the smaller value until the next reflow — which means a
    // bottom-anchored widget at offset_y=0 lands at the bottom of the
    // ORIGINAL viewport, not the monitor edge, and clips below.
    if let Some((w, h)) = chosen_size {
        gtk_window.set_default_size(w as i32, h as i32);
        let _ = window.set_size(tauri::LogicalSize::new(w, h));
    }
}

#[cfg(target_os = "linux")]
fn parse_anchor(s: &str) -> (gtk_layer_shell::Edge, gtk_layer_shell::Edge) {
    use gtk_layer_shell::Edge;
    let s = s.to_ascii_lowercase();
    let vert = if s.contains("top") {
        Edge::Top
    } else {
        Edge::Bottom
    };
    let horiz = if s.contains("right") {
        Edge::Right
    } else {
        Edge::Left
    };
    (vert, horiz)
}
