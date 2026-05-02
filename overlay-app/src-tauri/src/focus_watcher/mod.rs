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
