//! Foreground-window detection for the overlay.
//!
//! Polls the OS each tick, applies a MatchRule, and feeds the result
//! into a debounce state machine that emits focus-change messages to
//! every webview.

pub mod platform;

/// What we're matching against. Built once from --game-match or the
/// platform default.
#[derive(Debug, Clone)]
pub struct MatchRule {
    needle: String,
    strategy: MatchStrategy,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum MatchStrategy {
    /// Compare against the foreground process's exe basename
    /// (case-insensitive equals). Used on Windows.
    ExeEquals,
    /// Substring match against the foreground window's title
    /// (case-insensitive). Used on Linux + macOS.
    TitleSubstring,
}

/// What the platform query returns each tick.
#[derive(Debug, Clone)]
pub struct ForegroundInfo {
    /// Process image basename, lowercased.
    pub exe_name: Option<String>,
    /// Window title, lowercased.
    pub window_title: Option<String>,
    /// Foreground process PID. Used for the self-PID exception.
    pub pid: u32,
}

impl MatchRule {
    /// Construct from a user/CLI string. Empty needle disables matching
    /// (apply() always returns true) — the documented escape hatch.
    pub fn new(needle: &str, strategy: MatchStrategy) -> Self {
        Self {
            needle: needle.to_ascii_lowercase(),
            strategy,
        }
    }

    /// Whether the matcher is the always-true escape hatch.
    pub fn is_disabled(&self) -> bool {
        self.needle.is_empty()
    }

    /// Apply against a foreground info. Empty needle → true. Missing field
    /// for the chosen strategy → false (we can't match what we can't read).
    pub fn apply(&self, info: &ForegroundInfo) -> bool {
        if self.needle.is_empty() {
            return true;
        }
        match self.strategy {
            MatchStrategy::ExeEquals => match &info.exe_name {
                Some(name) => name == &self.needle,
                None => false,
            },
            MatchStrategy::TitleSubstring => match &info.window_title {
                Some(title) => title.contains(&self.needle),
                None => false,
            },
        }
    }
}

use std::time::{Duration, Instant};

/// Wait window before firing the hide event after "RL not foreground".
/// Zero = instant hide. Previously set to 150ms to absorb sub-frame
/// focus blips (Wayland tooltips, notification daemons), but the user
/// explicitly wants Alt-Tab to feel instant; a brief flicker on a
/// notification grabbing focus is acceptable in trade. If tooltip
/// flicker becomes a problem, the right fix is to detect "is the new
/// foreground a real top-level window or a transient popup" — not to
/// reintroduce a blanket delay.
pub const HIDE_DEBOUNCE: Duration = Duration::from_millis(0);

/// Internal state of the debouncer. The watcher owns one of these and feeds
/// query results in via `step()`.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum DebounceState {
    /// RL is foreground; overlay shown.
    Active,
    /// RL not foreground for ≥ HIDE_DEBOUNCE; overlay hidden.
    Inactive,
    /// RL not foreground but inside the debounce window. No event yet.
    PendingHide { since: Instant },
}

/// Outcome of a single tick. The watcher emits the boolean (if any) on the
/// Tauri channel.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct StepOutcome {
    pub next: DebounceState,
    pub emit: Option<bool>,
}

impl DebounceState {
    /// Start in `Inactive` so plugins that default hidden stay hidden
    /// until the watcher confirms RL is foreground. The first matching
    /// poll transitions to `Active` and emits `Some(true)` — instant
    /// show, asymmetric with the hide debounce.
    pub fn initial() -> Self {
        Self::Inactive
    }

    /// Advance the state by one tick. `now` is injected so tests can
    /// supply fake time.
    pub fn step(self, matched: bool, now: Instant) -> StepOutcome {
        match (self, matched) {
            // Already active and still foreground → no-op.
            (Self::Active, true) => StepOutcome {
                next: Self::Active,
                emit: None,
            },
            // Active and just lost focus → start the debounce window.
            (Self::Active, false) => StepOutcome {
                next: Self::PendingHide { since: now },
                emit: None,
            },
            // In the debounce window and focus came back → cancel.
            (Self::PendingHide { .. }, true) => StepOutcome {
                next: Self::Active,
                emit: None,
            },
            // Still not foreground → either keep waiting or fire hide.
            (Self::PendingHide { since }, false) => {
                if now.duration_since(since) >= HIDE_DEBOUNCE {
                    StepOutcome {
                        next: Self::Inactive,
                        emit: Some(false),
                    }
                } else {
                    StepOutcome {
                        next: Self::PendingHide { since },
                        emit: None,
                    }
                }
            }
            // Inactive → focus came back → instant show.
            (Self::Inactive, true) => StepOutcome {
                next: Self::Active,
                emit: Some(true),
            },
            // Inactive → still not foreground → no-op.
            (Self::Inactive, false) => StepOutcome {
                next: Self::Inactive,
                emit: None,
            },
        }
    }
}

#[cfg(test)]
mod debounce_tests {
    use super::*;

    fn at(ms: u64) -> Instant {
        // Instant has no public constructor; offset from a fixed base.
        static BASE: std::sync::OnceLock<Instant> = std::sync::OnceLock::new();
        let base = *BASE.get_or_init(Instant::now);
        base + Duration::from_millis(ms)
    }

    #[test]
    fn active_stays_active_when_matched() {
        let s = DebounceState::Active;
        let r = s.step(true, at(0));
        assert_eq!(r.next, DebounceState::Active);
        assert_eq!(r.emit, None);
    }

    #[test]
    fn active_to_pending_when_not_matched() {
        let s = DebounceState::Active;
        let r = s.step(false, at(100));
        match r.next {
            DebounceState::PendingHide { since } => assert_eq!(since, at(100)),
            other => panic!("expected PendingHide, got {other:?}"),
        }
        assert_eq!(r.emit, None);
    }

    #[test]
    fn pending_holds_inside_debounce_window() {
        // Anything strictly less than HIDE_DEBOUNCE keeps us pending.
        // No-op when debounce is zero (current setting: instant hide).
        if HIDE_DEBOUNCE.is_zero() {
            return;
        }
        let s = DebounceState::PendingHide { since: at(0) };
        let r = s.step(false, at(HIDE_DEBOUNCE.as_millis() as u64 / 2));
        assert!(matches!(r.next, DebounceState::PendingHide { .. }));
        assert_eq!(r.emit, None);
    }

    #[test]
    fn pending_fires_hide_at_debounce_threshold() {
        let s = DebounceState::PendingHide { since: at(0) };
        let r = s.step(false, at(HIDE_DEBOUNCE.as_millis() as u64));
        assert_eq!(r.next, DebounceState::Inactive);
        assert_eq!(r.emit, Some(false));
    }

    #[test]
    fn pending_fires_hide_after_threshold() {
        let s = DebounceState::PendingHide { since: at(0) };
        let r = s.step(false, at((HIDE_DEBOUNCE.as_millis() as u64) + 700));
        assert_eq!(r.next, DebounceState::Inactive);
        assert_eq!(r.emit, Some(false));
    }

    #[test]
    fn pending_cancels_on_match_returning() {
        let s = DebounceState::PendingHide { since: at(0) };
        let r = s.step(true, at(100));
        assert_eq!(r.next, DebounceState::Active);
        assert_eq!(r.emit, None);
    }

    #[test]
    fn inactive_to_active_emits_true_immediately() {
        let s = DebounceState::Inactive;
        let r = s.step(true, at(0));
        assert_eq!(r.next, DebounceState::Active);
        assert_eq!(r.emit, Some(true));
    }

    #[test]
    fn inactive_stays_inactive_when_not_matched() {
        let s = DebounceState::Inactive;
        let r = s.step(false, at(0));
        assert_eq!(r.next, DebounceState::Inactive);
        assert_eq!(r.emit, None);
    }
}

#[cfg(test)]
mod match_rule_tests {
    use super::*;

    fn info(exe: Option<&str>, title: Option<&str>) -> ForegroundInfo {
        ForegroundInfo {
            exe_name: exe.map(|s| s.to_ascii_lowercase()),
            window_title: title.map(|s| s.to_ascii_lowercase()),
            pid: 1234,
        }
    }

    #[test]
    fn exe_equals_case_insensitive_input_lowercased_at_construction() {
        let r = MatchRule::new("RocketLeague.exe", MatchStrategy::ExeEquals);
        assert!(r.apply(&info(Some("rocketleague.exe"), None)));
    }

    #[test]
    fn exe_equals_no_match() {
        let r = MatchRule::new("RocketLeague.exe", MatchStrategy::ExeEquals);
        assert!(!r.apply(&info(Some("discord.exe"), None)));
    }

    #[test]
    fn exe_equals_missing_field_is_false() {
        let r = MatchRule::new("RocketLeague.exe", MatchStrategy::ExeEquals);
        assert!(!r.apply(&info(None, Some("rocket league"))));
    }

    #[test]
    fn title_substring_finds_within() {
        let r = MatchRule::new("Rocket League", MatchStrategy::TitleSubstring);
        assert!(r.apply(&info(None, Some("rocket league (64-bit, dx11, cooked)"))));
    }

    #[test]
    fn title_substring_case_insensitive() {
        let r = MatchRule::new("rocket league", MatchStrategy::TitleSubstring);
        assert!(r.apply(&info(None, Some("ROCKET LEAGUE"))));
    }

    #[test]
    fn title_substring_missing_field_is_false() {
        let r = MatchRule::new("Rocket League", MatchStrategy::TitleSubstring);
        assert!(!r.apply(&info(Some("rocketleague.exe"), None)));
    }

    #[test]
    fn empty_needle_always_true() {
        let r = MatchRule::new("", MatchStrategy::ExeEquals);
        assert!(r.apply(&info(None, None)));
        assert!(r.is_disabled());
    }

    /// Regression: with the previous "Rocket League" substring needle,
    /// any browser tab/window whose title mentioned the words "rocket
    /// league" tripped the focus watcher and made hide_when_unfocused
    /// plugins visible outside the game. The actual RL window title
    /// is always `Rocket League (64-bit, DX11, Cooked)` — the trailing
    /// space + open-paren is a structural marker incidental mentions
    /// don't reproduce. Pinning that here.
    #[test]
    fn default_linux_rule_rejects_browser_tab_mentioning_rl() {
        #[cfg(any(target_os = "linux", target_os = "macos"))]
        {
            let r = default_match_rule();
            // Real RL window: matches.
            assert!(r.apply(&info(None, Some("rocket league (64-bit, dx11, cooked)"))));
            // Browser tab on the toolkit's own GitHub page: must NOT match.
            assert!(!r.apply(&info(
                None,
                Some("s1gh/rltoolkit: plugin sdk and overlay framework for rocket league — zen browser")
            )));
            // Discord channel mentioning rocket league: must NOT match.
            assert!(!r.apply(&info(None, Some("• discord | #rocket-league | foo"))));
            // Random title without the structural marker: must NOT match.
            assert!(!r.apply(&info(None, Some("rocket league wiki"))));
        }
    }
}

use std::thread;
use tauri::{AppHandle, Manager};

// Focus-change delivery uses webview.eval → window.postMessage rather
// than app.emit() because Tauri 2's event listener is only exposed via
// the @tauri-apps/api JS package, which the toolkit's plain-JS SDK
// doesn't bundle. The SDK filters by `data.__rlt_focus__ === true`.

/// Polling fallback cadence. The Wayland backend wakes on the
/// compositor socket fd (see platform::wait_for_event), so it doesn't
/// burn this — it's only the ceiling on the wait when no events are
/// arriving. Other backends use this as the real poll cadence.
pub const POLL_INTERVAL: Duration = Duration::from_millis(100);

/// Per-platform default needle, used when --game-match wasn't passed.
///
/// Linux/macOS use a tighter substring than just "Rocket League". The
/// real RL window title is "Rocket League (64-bit, DX11, Cooked)", so
/// matching the literal "rocket league (" still hits RL but won't trip
/// on browser tabs, the toolkit's own dashboard, chat windows, or any
/// other window that merely mentions the words "rocket league". The
/// trailing space + "(" is a structural marker the in-game window
/// always emits and incidental mentions almost never reproduce.
pub fn default_match_rule() -> MatchRule {
    #[cfg(target_os = "windows")]
    {
        MatchRule::new("RocketLeague.exe", MatchStrategy::ExeEquals)
    }
    #[cfg(any(target_os = "linux", target_os = "macos"))]
    {
        MatchRule::new("Rocket League (", MatchStrategy::TitleSubstring)
    }
    #[cfg(not(any(target_os = "windows", target_os = "linux", target_os = "macos")))]
    {
        MatchRule::new("", MatchStrategy::TitleSubstring)
    }
}

/// Build a MatchRule from --game-match. None = platform default,
/// Some("") = disabled, Some(s) = that needle with the platform's
/// strategy.
pub fn match_rule_from_arg(arg: Option<&str>) -> MatchRule {
    match arg {
        None => default_match_rule(),
        Some(s) => {
            let strategy = if cfg!(target_os = "windows") {
                MatchStrategy::ExeEquals
            } else {
                MatchStrategy::TitleSubstring
            };
            MatchRule::new(s, strategy)
        }
    }
}

/// Spawn the watcher thread. No shutdown signal — process exit
/// reclaims it.
pub fn spawn(app: AppHandle, rule: MatchRule) {
    if rule.is_disabled() {
        crate::log_info!("[rl-widget] focus-gating disabled (--game-match=\"\")");
        return;
    }
    crate::log_info!("[rl-widget] focus watcher: event-driven (idle ceiling {:?}), hide debounce {:?}",
              IDLE_WAIT_CEILING, HIDE_DEBOUNCE);

    let self_pid = std::process::id();
    thread::Builder::new()
        .name("rlt-focus-watcher".to_string())
        .spawn(move || {
            // Catch panics so a buggy platform query exits the thread
            // gracefully instead of taking down the process. The
            // overlay reverts to always-visible.
            let _ = std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| {
                run_loop(app, rule, self_pid);
            }));
            crate::log_error!("[rl-widget] focus watcher exiting (panic or natural). \
                       Overlay will remain visible until restart.");
        })
        .expect("focus watcher thread spawn failed");
}

/// Hard cap on a single wait, applied when nothing time-sensitive is
/// pending. Long enough that an idle watcher doesn't wake unnecessarily
/// (event-driven platforms truly sleep here), short enough that if the
/// compositor connection silently drops we'll retry within a second
/// rather than getting stuck forever. The wait_for_event call also
/// returns immediately on EINTR / spurious wake, so this is a ceiling
/// not a floor.
const IDLE_WAIT_CEILING: Duration = Duration::from_secs(1);

fn run_loop(app: AppHandle, rule: MatchRule, self_pid: u32) {
    let mut state = DebounceState::initial();
    let mut last_emit_log_at: Option<Instant> = None;
    loop {
        let matched_opt = poll_once(&rule, self_pid);
        let now = Instant::now();
        if let Some(matched) = matched_opt {
            let outcome = state.clone().step(matched, now);
            state = outcome.next;
            if let Some(active) = outcome.emit {
                let emit_result = post_focus_message(&app, active);
                if let Err(e) = emit_result {
                    let should_log = match last_emit_log_at {
                        None => true,
                        Some(prev) => now.duration_since(prev) >= Duration::from_secs(1),
                    };
                    if should_log {
                        crate::log_warn!("[rl-widget] focus-change emit failed: {e}");
                        last_emit_log_at = Some(now);
                    }
                }
                let _ = active;
            }
        }
        // Block until the OS pushes a focus event or the next
        // time-sensitive state transition is due. In PendingHide we
        // need to wake by `since + HIDE_DEBOUNCE` so the hide actually
        // fires when the user has truly Alt-Tabbed away (no further
        // compositor event will arrive — the new window is just
        // sitting there with `activated`). Other states have no
        // pending timeout, so we wait up to IDLE_WAIT_CEILING.
        let timeout = match &state {
            DebounceState::PendingHide { since } => {
                let elapsed = Instant::now().saturating_duration_since(*since);
                HIDE_DEBOUNCE.saturating_sub(elapsed)
            }
            _ => IDLE_WAIT_CEILING,
        };
        platform::wait_for_event(timeout);
    }
}

/// Dispatch a focus-change postMessage into every webview the app
/// owns AND map/unmap the overlay window at the compositor level.
///
/// The JS postMessage drives plugin-side logic (focus.onChange in the
/// SDK — plugins pause timers, hide their bodies via display:none,
/// etc.). On its own that's not enough: even after every plugin body
/// goes display:none, the overlay Tauri window is still mapped on top
/// of whatever the user Alt-Tabbed to, and anything an iframe paints
/// (an outline, an in-flight animation frame, a plugin that doesn't
/// honour hide_when_unfocused) lingers visibly for the duration of
/// the eval → postMessage → iframe-postMessage → repaint chain.
///
/// Calling `window.hide()` on the overlay surface here removes that
/// chain entirely from the felt-latency budget: the compositor
/// unmaps the surface on the next commit, ~1 frame. On focus regain,
/// `window.show()` re-maps. We respect the launcher's overlay-enabled
/// toggle so we don't auto-show a window the user explicitly turned
/// off via the tray / overflow menu.
fn post_focus_message(app: &AppHandle, active: bool) -> Result<(), String> {
    // Hide via the WebView, not the window. Calling win.hide() on
    // Hyprland triggers the compositor's window-close animation (fade
    // out over ~150ms), so the overlay visibly lingers even though the
    // surface IS unmapping. Toggling display on the root element instead
    // keeps the surface mapped — WebKit just paints an empty transparent
    // frame on the next commit, no compositor animation involved.
    //
    // We respect the launcher's overlay-enabled toggle on the show path
    // so we don't auto-show contents that the user explicitly turned off
    // via the tray. On the hide path we don't gate: if the user already
    // disabled the overlay, the contents are already hidden and a second
    // hide is a no-op.
    let user_wants_overlay = app
        .try_state::<crate::launcher::ipc::LauncherState>()
        .map(|state| state.lock().map(|ctx| ctx.overlay_enabled).unwrap_or(true))
        .unwrap_or(true);
    let should_show_contents = active && user_wants_overlay;
    let display_js = if should_show_contents {
        // Empty string lets the stylesheet's default win again.
        "document.documentElement.style.display='';"
    } else {
        "document.documentElement.style.display='none';"
    };

    let post_js = format!(
        "window.postMessage({{ __rlt_focus__: true, active: {} }}, '*');",
        if active { "true" } else { "false" }
    );

    let mut errors: Vec<String> = Vec::new();
    let mut sent_to_at_least_one = false;
    for (label, webview) in app.webviews().iter() {
        // Only flip display on the overlay webview ("main"). Other
        // webviews (the launcher) get the focus postMessage only —
        // applying display:none to the launcher would hide the
        // launcher whenever RL loses focus, which is plainly wrong.
        // Order matters for the overlay: flip display FIRST so the
        // next compositor commit already reflects the new visibility,
        // THEN post the focus event for plugin-side logic (pause
        // timers, etc). The reverse order would let plugin handlers
        // run a frame before the visual change landed.
        let js = if label == "main" {
            format!("{display_js}{post_js}")
        } else {
            post_js.clone()
        };
        match webview.eval(&js) {
            Ok(_) => sent_to_at_least_one = true,
            Err(e) => errors.push(format!("{label}: {e}")),
        }
    }
    if sent_to_at_least_one {
        Ok(())
    } else if errors.is_empty() {
        Err("no webviews to post to".to_string())
    } else {
        Err(errors.join("; "))
    }
}

/// One poll cycle. None = no signal this tick (transient query
/// failure or self-PID — state unchanged); Some(b) = RL is/isn't
/// foreground.
fn poll_once(rule: &MatchRule, self_pid: u32) -> Option<bool> {
    let info = platform::query_foreground()?;
    if info.pid == self_pid {
        return None;
    }
    Some(rule.apply(&info))
}
