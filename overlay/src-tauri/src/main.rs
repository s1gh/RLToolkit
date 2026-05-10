// rl-widget — overlay widget for RL Toolkit.
//
// Two modes: unified (one fullscreen window loading the toolkit's
// /overlay aggregator, which positions all enabled plugins) and
// per-plugin (--plugin=<name>, one window sized and anchored from the
// plugin's manifest). The window is transparent, frameless,
// click-through, and pinned above all others — exit happens via the
// tray icon's Quit item.
//
// Per-platform overlay primitive:
//   Linux/Wayland: wlr-layer-shell (overlay layer + anchored margins).
//     Click-through is NOT free — layer-shell only handles placement;
//     pointer input still goes to the GTK window unless we install an
//     empty input shape on top.
//   Windows / X11 / macOS: regular always-on-top frameless window with
//     ignore_cursor_events=true.
//
// Windows 11 also paints a hairline DWM frame that survives
// decorations(false). apply_windows_no_border turns off the relevant
// DWM attributes and strips WS_THICKFRAME.
//
// Plugins can reshape their widget at runtime via Tauri commands
// (RLT.widget.*) — see the `widget_*` handlers.
//
// CLI:
//   rl-widget [--plugin=<name>] [--toolkit=URL]

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
    // The view discriminator lives on the <script data-view="overlay">
    // tag inside the file; the URL only carries runtime info the SDK
    // can't derive (anchor for body alignment).
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

/// Liveness check for unified mode. Treated as fatal because the
/// fullscreen click-through window is unrecoverable from the desktop
/// — if it opens on a webview-error page, the user has no way to
/// close it short of Task Manager.
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
/// /api/overlay/surface/detected. Informational only — failures are
/// swallowed because the overlay can mount before the backend
/// finishes spawning. On multi-monitor setups the overlay may be on a
/// different monitor than the launcher window, so this is the more
/// accurate value when both succeed.
fn report_detected_surface(toolkit: &str, w: f64, h: f64) {
    let base = toolkit.trim_end_matches('/');
    let url = format!("{}/api/overlay/surface/detected", base);
    let body = format!(
        r#"{{"width":{},"height":{}}}"#,
        w.round() as i64,
        h.round() as i64
    );
    let _ = ureq::post(&url)
        .timeout(std::time::Duration::from_secs(2))
        .set("Content-Type", "application/json")
        .send_string(&body);
}

// ─── Tauri commands (the RLT.widget.* surface) ──────────────────
//
// Each command applies the change to Tauri's window (Windows/macOS)
// and the layer-shell surface (Linux, which uses its own anchor/margin
// protocol), updates shared WidgetState, and no-ops in unified mode
// (where per-plugin reshape doesn't apply).

/// Returns true when the widget is in unified mode.
fn ignored_in_unified(mode: &tauri::State<'_, Mode>, fn_name: &str) -> bool {
    if matches!(**mode, Mode::Unified) {
        rl_widget::log_debug!("[rl-widget] {} ignored in unified mode", fn_name);
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
        // Tauri's set_opacity is gated behind a feature; no-op for now
        // so the API surface stays uniform across platforms.
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

/// Unified-mode fullscreen pass on non-Linux: size to the monitor's
/// logical bounds, position at (0,0), then set_fullscreen to escape
/// Windows 11's work-area clipping (otherwise the taskbar reserves
/// ~48px and a bottom-anchored widget at offset_y=0 lands behind it).
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
    let _ = window.set_position(LogicalPosition::new(0.0, 0.0));
    let _ = window.set_size(LogicalSize::new(mon_w, mon_h));
    // macOS would invoke the green-button "Full Screen" mode here,
    // which animates and isn't what we want for an overlay.
    #[cfg(target_os = "windows")]
    let _ = window.set_fullscreen(true);
}

/// Suppress the Windows 11 hairline DWM frame around the overlay
/// window. The frame is compositor-painted, so HTML/CSS and
/// `decorations(false)` can't reach it. Four independent best-effort
/// knobs, harmless on Windows builds that don't recognize them:
///
///   1. DWMWA_BORDER_COLOR = DWMWA_COLOR_NONE — Win11 22000+.
///   2. DWMWA_WINDOW_CORNER_PREFERENCE = DWMWCP_DONOTROUND — opts out
///      of the rounded-corner pass that can paint a frame.
///   3. DWMWA_NCRENDERING_POLICY = DWMNCRP_DISABLED — kills DWM-drawn
///      frame chrome including the drop-shadow edge.
///   4. Strip WS_THICKFRAME (the 1px sizing border kept by
///      decorations(false)) and re-apply with SWP_FRAMECHANGED.
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
    // Tauri returns windows::Win32::Foundation::HWND (tuple struct
    // around *mut c_void); windows-sys aliases HWND to the same raw
    // pointer, so the inner field is what the Win32 calls expect.
    let hwnd: HWND = hwnd.0;

    // Safety for the unsafe blocks below: hwnd is a valid window handle
    // owned by Tauri for this window's lifetime. DwmSetWindowAttribute
    // reads `cbAttribute` bytes synchronously; each attribute here is a
    // 32-bit enum value.

    // (1) DWMWA_BORDER_COLOR = DWMWA_COLOR_NONE.
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
    // SWP_FRAMECHANGED forces DWM to recompute the non-client area;
    // NOMOVE|NOSIZE|NOZORDER|NOACTIVATE keeps everything else in place.
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

/// Build the tray icon with a Quit menu item. Best-effort: errors
/// bubble up so the caller can log and continue.
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
/// Unified mode with hardcoded defaults; the launcher owns the
/// lifecycle so tray and focus-watcher setup are skipped here.
fn build_overlay_for_launcher(app: &AppHandle) -> Result<(), String> {
    use tauri::Manager;

    if let Some(w) = app.get_webview_window("main") {
        let _ = w.show();
        return Ok(());
    }

    let toolkit_url = if let Some(state) = app.try_state::<crate::launcher::ipc::LauncherState>() {
        state.lock().unwrap().toolkit_url.clone()
    } else {
        "http://localhost:49200".to_string()
    };

    let url = unified_url(&toolkit_url);
    let title = "RL Toolkit – Overlay".to_string();

    build_overlay_window(app, &Mode::Unified, &url, &title, false, None, &toolkit_url)
        .map_err(|e| e.to_string())
}

/// Construct the overlay webview window inside the given app. Handles
/// both the overlay-only and launcher paths; the window is always
/// labelled `"main"`. `persist_cache=false` opens incognito so the
/// webview cache is discarded each launch. `monitor` is Linux/Wayland
/// only.
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

    // Built once during launcher setup() with visible(false), then
    // show()/hide()d on toggle. Building from an IPC worker thread
    // deadlocks WebView2 on Windows — only the main-thread setup()
    // path is safe.
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

    #[cfg(target_os = "windows")]
    apply_windows_no_border(&window);

    match mode {
        Mode::Plugin { manifest } => {
            #[cfg(target_os = "linux")]
            apply_layer_shell_plugin(&window, manifest, monitor);

            #[cfg(not(target_os = "linux"))]
            {
                // The launcher path doesn't manage WidgetState, so
                // apply_pixel_position can't run here. The window
                // appears at the default position; the toolkit
                // handles per-plugin layout.
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

    // Click-through must be applied AFTER set_fullscreen on Windows:
    // winit's fullscreen transition rewrites extended styles and clears
    // the WS_EX_TRANSPARENT bit set_ignore_cursor_events installed.
    #[cfg(not(target_os = "linux"))]
    {
        let _ = window.set_ignore_cursor_events(true);
    }

    // Caller decides visibility — launcher path stays hidden until toggled.
    Ok(())
}

fn main() {
    let args = Args::parse();

    if launcher_mode_active(&args) {
        // launcher::run installs file logging itself (reads the
        // user-configured data_dir from launcher.json). The standalone
        // overlay path below uses the platform default.
        rl_widget::overlay_bridge::install(|app| build_overlay_for_launcher(app));
        launcher::run(args);
        return;
    }

    rl_widget::logging::init(rl_widget::paths::default_data_dir());
    main_overlay(args);
}

fn main_overlay(args: Args) {
    let (mode, url, title) = match args.plugin.clone() {
        Some(name) if !name.is_empty() => {
            let manifest = match fetch_manifest(&args.toolkit, &name) {
                Ok(m) => m,
                Err(e) => {
                    rl_widget::log_error!("[rl-widget] {}", e);
                    rl_widget::log_error!("[rl-widget] start the toolkit first (e.g. ./rl-toolkit) and retry");
                    std::process::exit(1);
                }
            };
            let url = plugin_url(&args.toolkit, &name, &manifest.file, &manifest.anchor);
            rl_widget::log_info!("[rl-widget] plugin={} url={}", name, url);
            let title = format!("RL Toolkit – {}", name);
            (Mode::Plugin { manifest }, url, title)
        }
        _ => {
            if let Err(e) = probe_toolkit(&args.toolkit) {
                rl_widget::log_error!("[rl-widget] {}", e);
                rl_widget::log_error!("[rl-widget] start the toolkit first (e.g. ./rl-toolkit) and retry");
                std::process::exit(1);
            }
            let url = unified_url(&args.toolkit);
            rl_widget::log_info!("[rl-widget] unified mode url={}", url);
            (Mode::Unified, url, "RL Toolkit – Overlay".to_string())
        }
    };

    // Seed WidgetState from the manifest in Plugin mode. Unified mode
    // gets a default that's never read — widget_* handlers gate on
    // Mode and early-return.
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
    let tray_tooltip = title.clone();
    let args_for_setup = args.clone();

    tauri::Builder::default()
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
            // Tray is best-effort; minimal Linux desktops can't host one.
            if let Err(e) = setup_tray(app.handle(), &tray_tooltip) {
                rl_widget::log_warn!("[rl-widget] tray icon failed to register: {e}");
            }

            build_overlay_window(
                app.handle(),
                &mode_for_setup,
                &url,
                &title,
                args_for_setup.persist_cache,
                args_for_setup.monitor,
                &args_for_setup.toolkit,
            )?;

            // Plugin mode on non-Linux applies pixel positioning via the
            // managed WidgetState (only available in the overlay-only app).
            #[cfg(not(target_os = "linux"))]
            if let Mode::Plugin { .. } = &mode_for_setup {
                if let Some(window) = app.get_webview_window("main") {
                    let state = app.state::<Mutex<WidgetState>>();
                    apply_pixel_position(&window, &state);
                }
            }

            // Standalone shows immediately; the launcher path stays hidden
            // until toggled (see launcher::mod::run).
            {
                use tauri::Manager;
                if let Some(window) = app.get_webview_window("main") {
                    let _ = window.show();
                }
            }

            // Foreground-window watcher; runs until the process exits.
            // Disabled when --game-match="".
            let rule = focus_watcher::match_rule_from_arg(args_for_setup.game_match.as_deref());
            focus_watcher::spawn(app.handle().clone(), rule);

            Ok(())
        })
        .run(tauri::generate_context!())
        .expect("error while running rl-widget");
}

// ─── Linux: wlr-layer-shell init ────────────────────────────────

/// Layer-shell setup shared by both modes: alpha-aware GTK surface,
/// overlay layer, no keyboard focus, no exclusive zone. Sizing and
/// anchoring are mode-specific.
///
/// `initial_size` is the (logical width, height) the GtkWindow should
/// believe it is BEFORE layer-shell init and BEFORE realize(). This is
/// load-bearing: if it isn't set first, GTK realizes the window at the
/// Tauri/GTK default (e.g. 800x600) and the very first layer-surface
/// commit paints one frame at that size. CSS that resolves against
/// viewport width — `right: <px>`, `100vw`, etc. — then briefly
/// computes against the wrong width and content visibly jumps once
/// the compositor's configure event delivers the real geometry. See
/// gtk-layer-shell issue #114 (the maintainer's recommendation is to
/// set size_request on the GtkWindow before realize).
#[cfg(target_os = "linux")]
fn init_layer_shell_common(
    window: &tauri::WebviewWindow,
    monitor_index: Option<usize>,
    initial_size: Option<(i32, i32)>,
) -> Option<gtk::Window> {
    use gtk::prelude::*;
    use gtk_layer_shell::{Layer, LayerShell};

    let gtk_window = match window.gtk_window() {
        Ok(w) => w,
        Err(e) => {
            rl_widget::log_error!("[rl-widget] gtk_window unavailable, skipping layer-shell: {e}");
            return None;
        }
    };

    if let Some(screen) = gtk::prelude::GtkWindowExt::screen(&gtk_window) {
        if let Some(visual) = screen.rgba_visual() {
            gtk_window.set_visual(Some(&visual));
        }
    }
    gtk_window.set_app_paintable(true);

    // Geometry FIRST, then layer-shell init, then realize. Both
    // size_request and default_size are needed: size_request fixes
    // the GTK toplevel's preferred size before the realize-time
    // configure round-trip, default_size covers the rare GTK path
    // that consults default_size during realize. See doc comment.
    if let Some((w, h)) = initial_size {
        gtk_window.set_size_request(w, h);
        gtk_window.set_default_size(w, h);
    }

    gtk_window.init_layer_shell();

    // Bind to a specific GDK monitor so wlr-layer-shell doesn't fan
    // the surface across every output. Primary by default; --monitor=N
    // overrides. Must be called AFTER init_layer_shell() above.
    let chosen_monitor = if let Some(display) = gtk::gdk::Display::default() {
        let mon = if let Some(n) = monitor_index {
            display.monitor(n as i32)
        } else {
            display.primary_monitor().or_else(|| display.monitor(0))
        };
        if mon.is_none() {
            rl_widget::log_warn!(
                "[rl-widget] could not resolve a GDK monitor (index={monitor_index:?}); \
                 layer surface will fan across all outputs"
            );
        }
        mon
    } else {
        rl_widget::log_warn!("[rl-widget] no default GDK display; layer surface will fan across all outputs");
        None
    };

    if let Some(ref monitor) = chosen_monitor {
        gtk_window.set_monitor(monitor);
        let label = monitor
            .model()
            .map(|m| m.to_string())
            .unwrap_or_else(|| format!("index {:?}", monitor_index.unwrap_or(0)));
        rl_widget::log_info!("[rl-widget] layer-shell bound to monitor: {label:?}");
    }

    gtk_window.set_layer(Layer::Overlay);
    gtk_window.set_keyboard_interactivity(false);
    // -1 means "ignore other layer-shell clients' exclusive zones".
    // Without this, panels like waybar push the overlay's bottom up by
    // their reserved height. (0 means "I claim no exclusive zone",
    // which is NOT the same.)
    gtk_window.set_exclusive_zone(-1);

    // Click-through: an empty input shape tells GDK the surface has
    // no interactive area, so the compositor routes input to whatever
    // is beneath. realize() must run first — input_shape_combine_region
    // silently no-ops on a not-yet-shown window.
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

    let Some(gtk_window) = init_layer_shell_common(
        window,
        monitor_index,
        Some((cfg.width as i32, cfg.height as i32)),
    ) else {
        return;
    };

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

    // Report the chosen monitor's logical size to the toolkit. Same
    // monitor selection as init_layer_shell_common.
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

    // Hand the monitor's logical size to init_layer_shell_common so it
    // can pin set_size_request + set_default_size BEFORE realize().
    // Without this, the first layer-surface commit paints one frame at
    // the GTK default (e.g. 800×600), and right-anchored content
    // resolves against that smaller width — flashing on the left half
    // of the monitor until the compositor's configure event arrives.
    let initial_size = chosen_size.map(|(w, h)| (w as i32, h as i32));

    let Some(gtk_window) = init_layer_shell_common(window, monitor_index, initial_size) else {
        return;
    };

    for e in [Edge::Top, Edge::Bottom, Edge::Left, Edge::Right] {
        gtk_window.set_anchor(e, true);
        gtk_window.set_layer_shell_margin(e, 0);
    }

    // Tauri-side size mirror. The GTK side is now sized correctly via
    // the size_request/default_size pair in init_layer_shell_common
    // (before realize). This call keeps Tauri's own state in sync with
    // what GTK believes, which matters for code paths that read the
    // window size from the Tauri API later.
    if let Some((w, h)) = chosen_size {
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
