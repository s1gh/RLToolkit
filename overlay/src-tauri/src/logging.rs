//! Dual-sink logging: every `log!` call writes to a per-day file under
//! `<data dir>/logs/launcher-YYYY-MM-DD.log` AND to stderr.
//!
//! The file is the canonical artifact for bug reports — when the user
//! reports "the launcher did X," that log directory is what we ask
//! them to zip and send. Stderr remains useful when running from a
//! terminal during development (`cargo run`, `make release`).
//!
//! Daily rotation is done by checking the local-day stamp on each
//! write. Older files are pruned at init (kept = `RETAIN_DAYS`). No
//! external crate; we synthesize the YYYY-MM-DD from SystemTime, which
//! is plenty of resolution for a per-day filename.
//!
//! Init contract: call `init()` once early in `main()`, before any
//! log!() macro fires. Calls before init silently drop the file half
//! and still print to stderr — so bootstrapping log lines aren't lost,
//! they just live only in the terminal.

use std::fs::{File, OpenOptions};
use std::io::Write;
use std::path::PathBuf;
use std::sync::{Mutex, OnceLock};
use std::time::{SystemTime, UNIX_EPOCH};

/// How many days of launcher-*.log files to keep on disk. Anything
/// older is deleted at init. 14 days is enough that a user reporting
/// a bug from "last week" still has the relevant logs, but keeps the
/// directory from growing unbounded for users who run the launcher
/// daily.
const RETAIN_DAYS: u64 = 14;

/// File name prefix. Lives next to backend-*.log and sidecar-*.log
/// inside the same logs/ directory.
const FILE_PREFIX: &str = "launcher-";

struct LogState {
    dir: PathBuf,
    /// (date_stamp, open file). Reopens when the date stamp rolls.
    current: Option<(String, File)>,
}

static STATE: OnceLock<Mutex<LogState>> = OnceLock::new();

/// Initialize the logger. Creates the logs/ directory under `data_dir`,
/// opens today's file in append mode, and sweeps files older than
/// RETAIN_DAYS. Idempotent: if called twice, the second call wins on
/// directory but the open file is reused.
pub fn init(data_dir: PathBuf) {
    let dir = data_dir.join("logs");
    let _ = std::fs::create_dir_all(&dir);
    sweep_old(&dir);

    let stamp = today_stamp();
    let file = open_for_stamp(&dir, &stamp);

    let state = LogState {
        dir,
        current: file.map(|f| (stamp, f)),
    };

    let _ = STATE.get_or_init(|| Mutex::new(state));
    // Idempotent: subsequent init() calls are a no-op. The first call
    // wins on directory; we don't try to redirect an in-flight log
    // sink at runtime because there's no use case (the data dir is
    // settled before any log line fires).
}

/// Returns the canonical log directory once init() has run.
pub fn log_dir() -> Option<PathBuf> {
    STATE.get().and_then(|c| c.lock().ok().map(|s| s.dir.clone()))
}

/// Today's UTC date stamp in YYYY-MM-DD form. Exposed so other parts
/// of the launcher (notably the sidecar capture path) can match the
/// logging module's rotation cadence without duplicating the
/// civil-from-days math.
pub fn today_date_stamp() -> String {
    today_stamp()
}

/// Internal: write one fully-formed line to file + stderr. Adds a
/// trailing newline if missing. The line should already include any
/// `[facet]` prefix.
pub fn write_line(line: &str) {
    // Always print to stderr first so a panic in the file path
    // doesn't lose the message.
    eprintln!("{line}");

    let Some(cell) = STATE.get() else {
        return;
    };
    let Ok(mut state) = cell.lock() else {
        return;
    };

    let stamp = today_stamp();
    let needs_reopen = match &state.current {
        Some((s, _)) => s != &stamp,
        None => true,
    };
    if needs_reopen {
        if let Some(f) = open_for_stamp(&state.dir, &stamp) {
            state.current = Some((stamp.clone(), f));
        }
    }

    if let Some((_, f)) = state.current.as_mut() {
        // Format: HH:MM:SS <line>\n. Date is in the filename, no need
        // to repeat it. Best-effort writes — if the file went away we
        // don't try to recover here, the next reopen attempt picks it up.
        let ts = time_stamp();
        let _ = writeln!(f, "{ts} {line}");
        let _ = f.flush();
    }
}

fn open_for_stamp(dir: &PathBuf, stamp: &str) -> Option<File> {
    let path = dir.join(format!("{FILE_PREFIX}{stamp}.log"));
    OpenOptions::new()
        .create(true)
        .append(true)
        .open(&path)
        .ok()
}

fn sweep_old(dir: &PathBuf) {
    let Ok(entries) = std::fs::read_dir(dir) else {
        return;
    };
    let cutoff_secs = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_secs())
        .unwrap_or(0)
        .saturating_sub(RETAIN_DAYS * 86_400);

    for entry in entries.flatten() {
        let path = entry.path();
        let Some(name) = path.file_name().and_then(|n| n.to_str()) else {
            continue;
        };
        if !name.starts_with(FILE_PREFIX) || !name.ends_with(".log") {
            continue;
        }
        let Ok(meta) = entry.metadata() else { continue };
        let Ok(modified) = meta.modified() else { continue };
        let Ok(secs) = modified.duration_since(UNIX_EPOCH) else {
            continue;
        };
        if secs.as_secs() < cutoff_secs {
            let _ = std::fs::remove_file(&path);
        }
    }
}

/// YYYY-MM-DD in UTC. Synthesized from SystemTime so we don't pull
/// in chrono just for a filename. The Go backend uses local time for
/// its date stamp; this UTC vs local mismatch is intentional — Rust's
/// stdlib has no timezone-aware date math, and adding chrono just
/// for log filenames isn't worth it. For users in non-UTC zones the
/// launcher and backend files for the same evening can carry
/// different dates near midnight; the timestamps inside each file
/// disambiguate, and bug-report zips include both anyway.
fn today_stamp() -> String {
    let secs = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_secs())
        .unwrap_or(0);
    let (y, m, d) = ymd_from_secs(secs);
    format!("{y:04}-{m:02}-{d:02}")
}

/// HH:MM:SS in UTC. Cheap, no allocations beyond the format string.
/// UTC keeps timestamps comparable across user reports without needing
/// to ship the user's tz with the log.
fn time_stamp() -> String {
    let secs = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_secs())
        .unwrap_or(0);
    let h = (secs / 3600) % 24;
    let m = (secs / 60) % 60;
    let s = secs % 60;
    format!("{h:02}:{m:02}:{s:02}")
}

/// Days-from-epoch → (year, month, day) using the civil-from-days
/// algorithm (Howard Hinnant). Works for any year ≥ 0.
fn ymd_from_secs(secs: u64) -> (i64, u32, u32) {
    let days = (secs / 86_400) as i64;
    // Shift so day 0 = 0000-03-01 (March-based year for the algorithm).
    let z = days + 719_468;
    let era = if z >= 0 { z } else { z - 146_096 } / 146_097;
    let doe = z - era * 146_097; // [0, 146096]
    let yoe = (doe - doe / 1460 + doe / 36524 - doe / 146_096) / 365; // [0, 399]
    let y = yoe + era * 400;
    let doy = doe - (365 * yoe + yoe / 4 - yoe / 100); // [0, 365]
    let mp = (5 * doy + 2) / 153; // [0, 11]
    let d = (doy - (153 * mp + 2) / 5 + 1) as u32; // [1, 31]
    let m = (if mp < 10 { mp + 3 } else { mp - 9 }) as u32; // [1, 12]
    let y = if m <= 2 { y + 1 } else { y };
    (y, m, d)
}

/// Log macros. Three levels: info (normal events), warn (recoverable
/// problems), error (something genuinely went wrong). All three write
/// the same way — the level is just a tag in the line. Keeping the
/// API shape close to `eprintln!` makes the rewrite mechanical.
#[macro_export]
macro_rules! log_info {
    ($($arg:tt)*) => {{
        $crate::logging::write_line(&format!("INFO  {}", format_args!($($arg)*)));
    }};
}

#[macro_export]
macro_rules! log_warn {
    ($($arg:tt)*) => {{
        $crate::logging::write_line(&format!("WARN  {}", format_args!($($arg)*)));
    }};
}

#[macro_export]
macro_rules! log_error {
    ($($arg:tt)*) => {{
        $crate::logging::write_line(&format!("ERROR {}", format_args!($($arg)*)));
    }};
}

/// Debug log — written to file but NOT to stderr. Used for chatty
/// per-event lines (focus changes, surface-detect retries) we want
/// available for forensics but not in the terminal.
#[macro_export]
macro_rules! log_debug {
    ($($arg:tt)*) => {{
        $crate::logging::write_line_file_only(&format!("DEBUG {}", format_args!($($arg)*)));
    }};
}

/// File-only counterpart used by the `log_debug!` macro. Same path as
/// `write_line` minus the stderr println — kept on the public surface
/// so the macro can reach it.
pub fn write_line_file_only(line: &str) {
    let Some(cell) = STATE.get() else {
        return;
    };
    let Ok(mut state) = cell.lock() else {
        return;
    };

    let stamp = today_stamp();
    let needs_reopen = match &state.current {
        Some((s, _)) => s != &stamp,
        None => true,
    };
    if needs_reopen {
        if let Some(f) = open_for_stamp(&state.dir, &stamp) {
            state.current = Some((stamp.clone(), f));
        }
    }

    if let Some((_, f)) = state.current.as_mut() {
        let ts = time_stamp();
        let _ = writeln!(f, "{ts} {line}");
        let _ = f.flush();
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn ymd_civil_known_dates() {
        // 1970-01-01 → secs 0
        assert_eq!(ymd_from_secs(0), (1970, 1, 1));
        // 2026-05-08 → 56 years and change after epoch
        // 2026-05-08 00:00:00 UTC = 1778198400
        assert_eq!(ymd_from_secs(1_778_198_400), (2026, 5, 8));
        // 2000-02-29 (leap day) = 951782400
        assert_eq!(ymd_from_secs(951_782_400), (2000, 2, 29));
    }
}
