//! Foreground-window detection for the overlay.
//!
//! Polls the OS each tick, applies a MatchRule, and feeds the result into a
//! debounce state machine. State transitions emit a Tauri event
//! ("rlt://focus-change") with payload { active: bool }.
//!
//! See docs/superpowers/specs/2026-05-02-foreground-detection-design.md.

pub mod platform;

/// What we're matching against. Built once from --game-match (or the
/// platform default).
#[derive(Debug, Clone)]
pub struct MatchRule {
    needle: String, // already lowercased
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

/// How long after seeing "RL not foreground" we wait before firing the hide
/// event. Quick Alt-Tabs (Discord glance, Steam overlay) shorter than this
/// don't flicker the overlay.
pub const HIDE_DEBOUNCE: Duration = Duration::from_millis(500);

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
    /// Initial state. We assume RL is foreground until proven otherwise so
    /// the overlay shows immediately on launch (the alternative — Inactive
    /// — would briefly hide the widget for the first 250ms after startup
    /// while the first poll runs).
    pub fn initial() -> Self {
        Self::Active
    }

    /// Advance the state by one tick.
    ///
    /// `matched` is whether the most recent query says RL is foreground.
    /// `now` is the current time, passed in (not read from `Instant::now()`)
    /// so tests can inject fake time.
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
        // Build all test instants relative to a single base so comparisons
        // are deterministic. Instant doesn't expose a constructor; we lean
        // on a base captured once and offset from it.
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
        let s = DebounceState::PendingHide { since: at(0) };
        let r = s.step(false, at(250));
        assert!(matches!(r.next, DebounceState::PendingHide { .. }));
        assert_eq!(r.emit, None);
    }

    #[test]
    fn pending_fires_hide_at_debounce_threshold() {
        let s = DebounceState::PendingHide { since: at(0) };
        let r = s.step(false, at(500));
        assert_eq!(r.next, DebounceState::Inactive);
        assert_eq!(r.emit, Some(false));
    }

    #[test]
    fn pending_fires_hide_after_threshold() {
        let s = DebounceState::PendingHide { since: at(0) };
        let r = s.step(false, at(1200));
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
}

use std::thread;
use tauri::{AppHandle, Manager};

/// We use webview.eval(...) → window.postMessage(...) instead of Tauri's
/// app.emit() because Tauri 2's event API (event.listen) is only exposed
/// via the @tauri-apps/api JS package, which the toolkit's plain-JS SDK
/// doesn't bundle. window.__TAURI_INTERNALS__ has invoke + ipc primitives
/// but no event listener — confirmed by reading tauri-2.11.0/scripts/core.js
/// and src/event/init.js. eval+postMessage is the simplest end-around.
///
/// The SDK filters incoming messages on `data.__rlt_focus__ === true` to
/// distinguish our messages from anything else (Tauri internals, third-
/// party scripts, the webview's own postMessage traffic).

/// How often we ask the OS what's foreground. Tuned so 4 polls/sec stays
/// well under 1% CPU on every supported platform.
pub const POLL_INTERVAL: Duration = Duration::from_millis(250);

/// Per-platform default needle. The watcher uses this when --game-match was
/// not passed.
pub fn default_match_rule() -> MatchRule {
    #[cfg(target_os = "windows")]
    {
        MatchRule::new("RocketLeague.exe", MatchStrategy::ExeEquals)
    }
    #[cfg(any(target_os = "linux", target_os = "macos"))]
    {
        MatchRule::new("Rocket League", MatchStrategy::TitleSubstring)
    }
    #[cfg(not(any(target_os = "windows", target_os = "linux", target_os = "macos")))]
    {
        // Unknown platform: behave as the empty-needle escape hatch.
        MatchRule::new("", MatchStrategy::TitleSubstring)
    }
}

/// Build a MatchRule from a user-supplied --game-match value. None →
/// platform default. Some(empty) → disabled. Some(s) → that needle with the
/// platform's strategy.
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

/// Spawn the background watcher thread. Owns a clone of AppHandle for the
/// app's lifetime; no shutdown signal — process exit reclaims the thread.
pub fn spawn(app: AppHandle, rule: MatchRule) {
    if rule.is_disabled() {
        eprintln!("[rl-widget] focus-gating disabled (--game-match=\"\")");
        return;
    }
    eprintln!("[rl-widget] focus watcher: poll every {:?}, hide debounce {:?}",
              POLL_INTERVAL, HIDE_DEBOUNCE);

    let self_pid = std::process::id();
    thread::Builder::new()
        .name("rlt-focus-watcher".to_string())
        .spawn(move || {
            // Catch panics so a bug in a platform query doesn't take down
            // the watcher silently. On panic we log once and exit the
            // thread; overlay reverts to always-visible.
            let _ = std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| {
                run_loop(app, rule, self_pid);
            }));
            eprintln!("[rl-widget] focus watcher exiting (panic or natural). \
                       Overlay will remain visible until restart.");
        })
        .expect("focus watcher thread spawn failed");
}

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
                        eprintln!("[rl-widget] focus-change emit failed: {e}");
                        last_emit_log_at = Some(now);
                    }
                }
                eprintln!("[rl-widget] focus → {}", if active { "active" } else { "inactive" });
            }
        }
        thread::sleep(POLL_INTERVAL);
    }
}

/// Find every webview window the app currently owns and dispatch a
/// `window.postMessage({ __rlt_focus__: true, active: <bool> }, '*')` into
/// each via webview.eval(). The SDK listens for `message` events with that
/// shape and fans them out to plugin onFocusChange handlers.
///
/// On a per-plugin overlay there's exactly one window ("main"); on a
/// unified overlay, also one. Iterating webviews keeps this future-proof
/// if we ever spawn additional surfaces.
fn post_focus_message(app: &AppHandle, active: bool) -> Result<(), String> {
    let js = format!(
        "window.postMessage({{ __rlt_focus__: true, active: {} }}, '*');",
        if active { "true" } else { "false" }
    );
    let mut errors: Vec<String> = Vec::new();
    let mut sent_to_at_least_one = false;
    for (label, webview) in app.webviews().iter() {
        match webview.eval(&js) {
            Ok(_) => {
                sent_to_at_least_one = true;
            }
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

/// One poll cycle. Returns:
///   None              — no signal this tick (transient query failure or self-PID).
///                       The debouncer treats this as "state unchanged."
///   Some(true|false)  — RL is / is not foreground.
fn poll_once(rule: &MatchRule, self_pid: u32) -> Option<bool> {
    let info = platform::query_foreground()?;
    if info.pid == self_pid {
        return None; // self-exception: never count our own process as RL
    }
    Some(rule.apply(&info))
}
