// rl-widget — per-plugin overlay window.
//
// One process per plugin. Loads the toolkit's plugin overlay URL into a
// transparent, frameless, click-through window pinned above all others.
//
// Per-platform overlay primitive:
//   Linux/Wayland: wlr-layer-shell (overlay layer + anchored margins). The
//     compositor handles placement; no windowrule config required.
//   Windows / X11 / macOS: regular always-on-top frameless window with
//     ignore_cursor_events=true. Tauri handles WS_EX_TRANSPARENT,
//     NSWindow.level, and X11 _NET_WM_STATE_ABOVE per platform.
//
// Plugins can also reshape their own widget at runtime via Tauri commands
// (RLT.widget.* in the SDK) — see the `widget_*` handlers below.
//
// CLI:
//   rl-widget --plugin=<name> [--toolkit=http://localhost:8080]

#![cfg_attr(all(not(debug_assertions), target_os = "windows"), windows_subsystem = "windows")]

use clap::Parser;
use serde::Deserialize;
use std::sync::Mutex;
#[cfg(not(target_os = "linux"))]
use tauri::{LogicalPosition, Manager};
use tauri::{LogicalSize, WebviewUrl, WebviewWindowBuilder};

#[derive(Parser, Debug, Clone)]
#[command(name = "rl-widget", about = "RL Toolkit overlay widget")]
struct Args {
    /// Plugin name — must exist in toolkit's /api/plugins response.
    /// When omitted, the widget runs in unified mode and loads the
    /// toolkit's /overlay aggregator page (all enabled plugins, one
    /// fullscreen click-through window).
    #[arg(long)]
    plugin: Option<String>,

    /// Toolkit base URL.
    #[arg(long, default_value = "http://localhost:8080")]
    toolkit: String,
}

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

fn fetch_manifest(toolkit: &str, plugin: &str) -> OverlayCfg {
    let url = format!("{}/api/plugins", toolkit.trim_end_matches('/'));
    let resp = match ureq::get(&url).timeout(std::time::Duration::from_secs(2)).call() {
        Ok(r) => r,
        Err(e) => {
            eprintln!("[rl-widget] manifest fetch failed: {e}");
            return default_overlay();
        }
    };
    let list: Vec<PluginManifest> = match resp.into_json() {
        Ok(v) => v,
        Err(e) => {
            eprintln!("[rl-widget] manifest parse failed: {e}");
            return default_overlay();
        }
    };
    list.into_iter()
        .find(|p| p.name == plugin)
        .map(|p| p.overlay)
        .unwrap_or_else(|| {
            eprintln!("[rl-widget] plugin {plugin:?} not in /api/plugins; using defaults");
            default_overlay()
        })
}

fn default_overlay() -> OverlayCfg {
    OverlayCfg {
        file: "overlay.html".into(),
        width: 320,
        height: 240,
        anchor: "bottom-left".into(),
        offset_x: 12,
        offset_y: 12,
    }
}

fn plugin_url(toolkit: &str, plugin: &str, file: &str, anchor: &str) -> String {
    let f = if file.is_empty() { "overlay.html" } else { file };
    format!(
        "{}/plugins/{}/{}?overlay=1&anchor={}",
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

/// Liveness check used by unified mode. Hits /api/status with the same
/// 2-second timeout as the per-plugin manifest fetch. Logs on failure
/// but never errors out — the webview will retry on its own once the
/// toolkit comes up.
fn probe_toolkit(toolkit: &str) {
    let url = format!("{}/api/status", toolkit.trim_end_matches('/'));
    match ureq::get(&url).timeout(std::time::Duration::from_secs(2)).call() {
        Ok(_) => {}
        Err(e) => eprintln!(
            "[rl-widget] toolkit unreachable at {}: {}; opening window anyway",
            toolkit.trim_end_matches('/'),
            e
        ),
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

#[tauri::command]
fn widget_size(
    width: u32,
    height: u32,
    window: tauri::WebviewWindow,
    state: tauri::State<'_, Mutex<WidgetState>>,
) -> Result<(), String> {
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
) -> Result<(), String> {
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
) -> Result<(), String> {
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
fn widget_opacity(opacity: f64, window: tauri::WebviewWindow) -> Result<(), String> {
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
fn widget_visible(visible: bool, window: tauri::WebviewWindow) -> Result<(), String> {
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
fn apply_linux_anchor(
    window: &tauri::WebviewWindow,
    state: &tauri::State<'_, Mutex<WidgetState>>,
) {
    use gtk_layer_shell::LayerShell;
    let snapshot = match state.lock() {
        Ok(s) => s.clone(),
        Err(_) => return,
    };
    let Ok(gtk_window) = window.gtk_window() else { return };

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
    let Ok(Some(monitor)) = window.current_monitor() else { return };
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

fn main() {
    let args = Args::parse();

    let (mode, url, title) = match args.plugin.clone() {
        Some(name) => {
            let manifest = fetch_manifest(&args.toolkit, &name);
            let url = plugin_url(&args.toolkit, &name, &manifest.file, &manifest.anchor);
            eprintln!("[rl-widget] plugin={} url={}", name, url);
            let title = format!("RL Toolkit – {}", name);
            (Mode::Plugin { manifest }, url, title)
        }
        None => {
            probe_toolkit(&args.toolkit);
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
            let parsed = url::Url::parse(&url).map_err(|e| format!("bad url {url}: {e}"))?;

            let mut builder = WebviewWindowBuilder::new(app, "main", WebviewUrl::External(parsed))
                .title(&title)
                .decorations(false)
                .transparent(true)
                .always_on_top(true)
                .skip_taskbar(true)
                .resizable(false)
                .focused(false)
                .visible(false);

            if let Mode::Plugin { manifest, .. } = &mode_for_setup {
                builder = builder.inner_size(manifest.width as f64, manifest.height as f64);
            }

            let window = builder.build()?;

            #[cfg(not(target_os = "linux"))]
            {
                let _ = window.set_ignore_cursor_events(true);
            }

            match &mode_for_setup {
                Mode::Plugin { manifest, .. } => {
                    #[cfg(target_os = "linux")]
                    apply_layer_shell(&window, manifest);

                    #[cfg(not(target_os = "linux"))]
                    {
                        let state = app.state::<Mutex<WidgetState>>();
                        apply_pixel_position(&window, &state);
                    }
                }
                Mode::Unified => {
                    // Sizing/anchoring lands in Task 4. For now the
                    // window opens at Tauri's default size/position;
                    // the next task replaces this with the real
                    // fullscreen pass.
                }
            }

            window.show()?;
            Ok(())
        })
        .run(tauri::generate_context!())
        .expect("error while running rl-widget");
}

// ─── Linux: wlr-layer-shell init ────────────────────────────────
#[cfg(target_os = "linux")]
fn apply_layer_shell(window: &tauri::WebviewWindow, cfg: &OverlayCfg) {
    use gtk::prelude::*;
    use gtk_layer_shell::{Layer, LayerShell};

    let gtk_window = match window.gtk_window() {
        Ok(w) => w,
        Err(e) => {
            eprintln!("[rl-widget] gtk_window unavailable, skipping layer-shell: {e}");
            return;
        }
    };

    // Make the GTK surface alpha-aware before layer-shell binds it.
    if let Some(screen) = gtk::prelude::GtkWindowExt::screen(&gtk_window) {
        if let Some(visual) = screen.rgba_visual() {
            gtk_window.set_visual(Some(&visual));
        }
    }
    gtk_window.set_app_paintable(true);

    gtk_window.init_layer_shell();
    gtk_window.set_layer(Layer::Overlay);
    gtk_window.set_keyboard_interactivity(false);
    gtk_window.set_exclusive_zone(0);

    gtk_window.set_size_request(cfg.width as i32, cfg.height as i32);
    gtk_window.set_default_size(cfg.width as i32, cfg.height as i32);
    gtk_window.set_resizable(false);

    let (vert_edge, horiz_edge) = parse_anchor(&cfg.anchor);
    gtk_window.set_anchor(vert_edge, true);
    gtk_window.set_anchor(horiz_edge, true);
    gtk_window.set_layer_shell_margin(vert_edge, cfg.offset_y);
    gtk_window.set_layer_shell_margin(horiz_edge, cfg.offset_x);
}

#[cfg(target_os = "linux")]
fn parse_anchor(s: &str) -> (gtk_layer_shell::Edge, gtk_layer_shell::Edge) {
    use gtk_layer_shell::Edge;
    let s = s.to_ascii_lowercase();
    let vert = if s.contains("top") { Edge::Top } else { Edge::Bottom };
    let horiz = if s.contains("right") { Edge::Right } else { Edge::Left };
    (vert, horiz)
}
