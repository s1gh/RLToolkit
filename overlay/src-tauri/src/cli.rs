//! CLI argument parsing for rl-widget. Shared between the overlay-only
//! path (in main.rs) and the launcher path (in launcher/mod.rs).

use clap::Parser;

#[derive(Parser, Debug, Clone)]
#[command(name = "rl-widget", about = "RL Toolkit overlay widget")]
pub struct Args {
    /// Plugin name — must exist in toolkit's /api/plugins response.
    /// When omitted, the widget runs in unified mode and loads the
    /// toolkit's /overlay aggregator page (all enabled plugins, one
    /// fullscreen click-through window).
    #[arg(long)]
    pub plugin: Option<String>,

    /// Toolkit base URL.
    #[arg(long, default_value = "http://localhost:8080")]
    pub toolkit: String,

    /// Global hotkey to quit the overlay. Format follows Tauri's
    /// shortcut syntax — e.g. "Ctrl+Shift+Q", "Alt+F4", "CmdOrCtrl+W".
    /// Required: the overlay window is unfocusable, undecorated, and
    /// click-through, so the hotkey is the user's guaranteed exit.
    /// If registration fails (already taken by another app), startup
    /// aborts so the user picks something else.
    #[arg(long, default_value = "Ctrl+Shift+Q")]
    pub quit_hotkey: String,

    /// Window-title substring (Linux/macOS) or exe basename (Windows) that
    /// identifies the game window. Defaults are platform-specific:
    /// "RocketLeague.exe" on Windows, "Rocket League" on Linux/macOS.
    /// Pass an empty string to disable focus-gating entirely (overlay always
    /// shown).
    #[arg(long)]
    pub game_match: Option<String>,

    /// Output index to bind the overlay to on Linux/Wayland (0-based,
    /// matches the order in `hyprctl monitors` or compositor enumeration).
    /// Default: primary monitor. Ignored on Windows/macOS — those use the
    /// current monitor at window-build time.
    ///
    /// Why this exists: wlr-layer-shell layer surfaces with no bound
    /// output are fanned across all connected outputs. Binding to a
    /// specific monitor pins the overlay to one screen.
    #[arg(long)]
    pub monitor: Option<usize>,

    /// Persist the webview's HTTP cache, cookies, and storage between
    /// sessions. Default OFF — plugin assets change frequently during
    /// development, and webkit2gtk's heuristic freshness window has been
    /// observed to serve stale CSS even when the server sends
    /// Cache-Control: no-cache. Running incognito gives every launch a
    /// clean slate. Pass `--persist-cache` only when a plugin you trust
    /// actually relies on persistent webview state (cookies, IndexedDB,
    /// etc.).
    #[arg(long)]
    pub persist_cache: bool,

    /// Run the launcher (control panel) instead of the overlay-only path.
    /// Defaulted to true when no other overlay-shaping flag is passed; pass
    /// `--no-launcher` to force overlay-only behavior.
    #[arg(long)]
    pub launcher: bool,

    /// Force overlay-only behavior, suppressing the launcher window even
    /// when no other flags are passed.
    #[arg(long)]
    pub no_launcher: bool,
}
