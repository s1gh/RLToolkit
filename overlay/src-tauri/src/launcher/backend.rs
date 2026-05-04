//! Backend lifecycle: probe, sidecar spawn, graceful + forceful terminate.
//!
//! The launcher uses `probe_status` to decide whether to attach to an
//! already-running backend or spawn its own sidecar. The validating
//! struct mirrors the existing `/api/status` shape from
//! `backend/server.go:344` — `{"rl_api": <RLStatus>}`. Any deserialization
//! failure means "something else is on this port."

use serde::Deserialize;
use std::time::Duration;

#[derive(Debug, PartialEq, Eq)]
pub enum ProbeOutcome {
    /// 200 + JSON shape we recognize as the toolkit.
    Toolkit,
    /// 200 (or non-error) but the body is not the toolkit's shape.
    Unrelated,
    /// Connection refused, timeout, or other transport error.
    Unreachable,
}

#[derive(Deserialize)]
struct StatusEnvelope {
    #[allow(dead_code)]
    rl_api: serde_json::Value,
}

pub fn probe_status(url: &str, timeout: Duration) -> ProbeOutcome {
    let client = match reqwest::blocking::Client::builder()
        .timeout(timeout)
        .build()
    {
        Ok(c) => c,
        Err(_) => return ProbeOutcome::Unreachable,
    };

    let resp = match client.get(url).send() {
        Ok(r) => r,
        Err(_) => return ProbeOutcome::Unreachable,
    };

    if !resp.status().is_success() {
        return ProbeOutcome::Unrelated;
    }

    match resp.json::<StatusEnvelope>() {
        Ok(_) => ProbeOutcome::Toolkit,
        Err(_) => ProbeOutcome::Unrelated,
    }
}

use std::process::Child;

/// Tracks who started the running backend. The launcher only kills
/// `Spawned` children; backends discovered via probe (`Attached`) are
/// left running on quit. `Unavailable` means we tried and gave up.
#[derive(Debug)]
pub enum BackendOwnership {
    Spawned(Child),
    Attached,
    Unavailable,
}

impl BackendOwnership {
    /// SIGTERM-then-grace-then-SIGKILL on Unix; equivalent on Windows via
    /// the child's kill API (Tauri's sidecar machinery wraps both).
    /// No-op for `Attached` and `Unavailable`.
    pub fn terminate(&mut self, grace: std::time::Duration) {
        let child = match self {
            BackendOwnership::Spawned(c) => c,
            _ => return,
        };

        #[cfg(unix)]
        {
            use std::os::unix::process::ExitStatusExt;
            let pid = child.id() as i32;
            // SIGTERM first.
            unsafe { libc::kill(pid, libc::SIGTERM) };

            let deadline = std::time::Instant::now() + grace;
            while std::time::Instant::now() < deadline {
                match child.try_wait() {
                    Ok(Some(_)) => return,
                    Ok(None) => std::thread::sleep(std::time::Duration::from_millis(50)),
                    Err(_) => break,
                }
            }
            // Still alive — SIGKILL.
            let _ = child.kill();
            let _ = child.wait();
            // Touch the unused import so non-test builds don't warn.
            let _ = <std::process::ExitStatus as ExitStatusExt>::signal;
        }

        #[cfg(windows)]
        {
            // Windows has no SIGTERM analogue we can issue cleanly from
            // a child handle without console attachment. The Tauri shell
            // plugin's sidecar API exposes a graceful close; for v1 we
            // try `kill()` (which posts WM_CLOSE on a console process or
            // calls TerminateProcess), and rely on Go's signal.Notify(SIGINT)
            // path being installed for builds run in a console. The 2 s
            // grace is approximated by waiting on the child after kill.
            let _ = child.kill();
            let deadline = std::time::Instant::now() + grace;
            while std::time::Instant::now() < deadline {
                match child.try_wait() {
                    Ok(Some(_)) => return,
                    Ok(None) => std::thread::sleep(std::time::Duration::from_millis(50)),
                    Err(_) => break,
                }
            }
            let _ = child.wait();
        }
    }
}
