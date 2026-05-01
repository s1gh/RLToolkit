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
// CLI:
//   rl-widget --plugin=<name> [--toolkit=http://localhost:8080]

#![cfg_attr(all(not(debug_assertions), target_os = "windows"), windows_subsystem = "windows")]

use clap::Parser;
use serde::Deserialize;
use tauri::{WebviewUrl, WebviewWindowBuilder};

#[derive(Parser, Debug, Clone)]
#[command(name = "rl-widget", about = "RL Toolkit per-plugin overlay widget")]
struct Args {
    /// Plugin name — must exist in toolkit's /api/plugins response.
    #[arg(long)]
    plugin: String,

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

/// Fetch the plugin's manifest from the toolkit. Best-effort — if the toolkit
/// isn't up yet or the plugin is unknown, returns sensible defaults so the
/// window still renders (it'll just sit empty until the toolkit comes up).
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

/// Tiny URL-component encoder — only encodes the characters our anchor
/// strings could ever contain ("-"). Avoids pulling in url/percent-encoding
/// crates for a value we control.
fn urlencoding_minimal(s: &str) -> String {
    s.replace(' ', "%20")
}

fn main() {
    let args = Args::parse();
    let manifest = fetch_manifest(&args.toolkit, &args.plugin);

    let url = plugin_url(&args.toolkit, &args.plugin, &manifest.file, &manifest.anchor);
    eprintln!("[rl-widget] plugin={} url={}", args.plugin, url);

    let plugin_name = args.plugin.clone();
    let manifest_for_setup = manifest.clone();

    tauri::Builder::default()
        .setup(move |app| {
            let title = format!("RL Toolkit – {}", plugin_name);
            let parsed = url::Url::parse(&url).map_err(|e| format!("bad url {url}: {e}"))?;

            let window = WebviewWindowBuilder::new(
                app,
                "main",
                WebviewUrl::External(parsed),
            )
            .title(&title)
            .inner_size(manifest_for_setup.width as f64, manifest_for_setup.height as f64)
            .decorations(false)
            .transparent(true)
            .always_on_top(true)
            .skip_taskbar(true)
            .resizable(false)
            .focused(false)
            .visible(false) // shown after platform-specific setup runs
            .build()?;

            // True click-through: input events pass to the window beneath.
            // Works on Windows (WS_EX_TRANSPARENT), macOS
            // (ignoresMouseEvents), and X11. On Wayland it's a no-op — we
            // get click-through from layer-shell's keyboard_interactivity
            // setting below.
            let _ = window.set_ignore_cursor_events(true);

            #[cfg(target_os = "linux")]
            apply_layer_shell(&window, &manifest_for_setup);

            window.show()?;
            Ok(())
        })
        .run(tauri::generate_context!())
        .expect("error while running rl-widget");
}

// ─── Linux: wlr-layer-shell integration ─────────────────────────
//
// gtk-layer-shell takes a GtkWindow and turns it into a layer-shell surface
// before the window is mapped. Tauri exposes the underlying gtk::Window via
// WebviewWindow::gtk_window(). We:
//   1. Mark the window for the OVERLAY layer (above all normal windows).
//   2. Anchor it to the manifest's corner (e.g. bottom-left).
//   3. Set margin from each anchored edge to the manifest's offset_x/offset_y.
//   4. keyboard_interactivity = NONE → no focus, true click-through.
//   5. exclusive_zone = 0 → don't reserve screen space; we float over content.
//
// On compositors that don't implement wlr-layer-shell (rare on Wayland, but
// e.g. GNOME's mutter doesn't), gtk-layer-shell logs a warning and the
// window falls back to xdg-toplevel — still always-on-top via Tauri, just
// without the anchored placement.
#[cfg(target_os = "linux")]
fn apply_layer_shell(window: &tauri::WebviewWindow, cfg: &OverlayCfg) {
    use gtk_layer_shell::{Layer, LayerShell};

    let gtk_window = match window.gtk_window() {
        Ok(w) => w,
        Err(e) => {
            eprintln!("[rl-widget] gtk_window unavailable, skipping layer-shell: {e}");
            return;
        }
    };

    gtk_window.init_layer_shell();
    gtk_window.set_layer(Layer::Overlay);
    // No keyboard input → true click-through at the protocol level.
    // (set_keyboard_mode + KeyboardMode::None would do the same and is
    // newer, but it requires gtk-layer-shell's v0_5 feature flag.)
    gtk_window.set_keyboard_interactivity(false);
    gtk_window.set_exclusive_zone(0);

    // Anchor the window to the requested corner. Layer-shell anchors are
    // edges (TOP, BOTTOM, LEFT, RIGHT); a corner is the intersection of
    // two edges. Margin-from-anchored-edge gives us padding.
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

