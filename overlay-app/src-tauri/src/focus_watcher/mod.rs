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
