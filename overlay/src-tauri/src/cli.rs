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
    #[arg(long, default_value = "http://localhost:49200")]
    pub toolkit: String,

    /// Window-title substring (Linux/macOS) or exe basename (Windows) that
    /// identifies the game window. Defaults are platform-specific:
    /// "RocketLeague.exe" on Windows, "Rocket League" on Linux/macOS.
    /// Pass an empty string to disable focus-gating entirely (overlay always
    /// shown).
    #[arg(long)]
    pub game_match: Option<String>,

    /// Output index to bind the overlay to on Linux/Wayland (0-based,
    /// matches the order in `hyprctl monitors`). Default: primary
    /// monitor. Ignored on Windows/macOS. Without this, wlr-layer-shell
    /// fans the surface across every output.
    #[arg(long)]
    pub monitor: Option<usize>,

    /// Persist the webview's HTTP cache, cookies, and storage between
    /// sessions. Default OFF (incognito) so plugin asset changes
    /// always pick up — webkit2gtk's heuristic freshness window can
    /// serve stale CSS even with Cache-Control: no-cache.
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
