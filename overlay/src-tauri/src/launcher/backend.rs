//! Backend lifecycle: probe, sidecar spawn, graceful + forceful terminate.
//!
//! The launcher uses `probe_status` to decide whether to attach to an
//! already-running backend or spawn its own sidecar. The validating
//! struct mirrors the existing `/api/status` shape from
//! `backend/server.go:344` — `{"rl_api": <RLStatus>}`. Any deserialization
//! failure means "something else is on this port."

use serde::Deserialize;
use std::path::PathBuf;
use std::time::Duration;

/// Path to today's sidecar capture log. The Go backend now owns its
/// own structured `backend-YYYY-MM-DD.log` (see backend/internal/logging),
/// so this file is purely a safety net for stdout/stderr that bypasses
/// `log.Printf` — Go runtime panics, raw `fmt.Print`, stack traces.
/// Same daily-stamp scheme so all three streams (launcher / backend /
/// sidecar) line up when zipped for a bug report.
pub fn sidecar_log_path_today() -> PathBuf {
    let stamp = crate::logging::today_date_stamp();
    crate::paths::sidecar_log_path(&stamp)
}

#[derive(Debug, PartialEq, Eq)]
pub enum ProbeOutcome {
    /// 200 + JSON shape we recognize as the toolkit.
    Toolkit { rl_api: String },
    /// 200 (or non-error) but the body is not the toolkit's shape.
    Unrelated,
    /// Connection refused, timeout, or other transport error.
    Unreachable,
}

#[derive(Deserialize)]
struct StatusEnvelope {
    rl_api: String,
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
        Ok(env) => ProbeOutcome::Toolkit { rl_api: env.rl_api },
        Err(_) => ProbeOutcome::Unrelated,
    }
}

use tauri::AppHandle;
use tauri_plugin_shell::process::{CommandChild, CommandEvent};
use tauri_plugin_shell::ShellExt;

/// Tracks who started the running backend. `SpawnedSidecar` holds a real
/// `tauri_plugin_shell` child (production); `SpawnedRaw` holds a
/// `std::process::Child` (integration tests using a fake binary). `Attached`
/// and `Unavailable` are no-ops on terminate. `StoppedByUser` is a deliberate
/// stop the user requested — distinct from `Unavailable` (which means we
/// tried and gave up) so the UI can show "Start backend" rather than
/// "Backend crashed".
#[derive(Debug)]
pub enum BackendOwnership {
    SpawnedSidecar(CommandChild),
    SpawnedRaw(std::process::Child),
    Attached,
    Unavailable,
    StoppedByUser,
}

// Convenience constructor for tests.
impl BackendOwnership {
    pub fn from_raw(c: std::process::Child) -> Self {
        BackendOwnership::SpawnedRaw(c)
    }
}

impl BackendOwnership {
    pub fn terminate(&mut self, grace: std::time::Duration) {
        match self {
            BackendOwnership::SpawnedRaw(child) => {
                Self::terminate_raw(child, grace);
            }
            BackendOwnership::SpawnedSidecar(_) => {
                // CommandChild::kill consumes self. Swap out the variant so we
                // can take ownership, then call kill on the owned child.
                let owned = std::mem::replace(self, BackendOwnership::Unavailable);
                if let BackendOwnership::SpawnedSidecar(child) = owned {
                    let pid = child.pid();
                    let deadline = std::time::Instant::now() + grace;
                    // Soft kill; CommandChild::kill posts the platform terminate signal.
                    let _ = child.kill();
                    while std::time::Instant::now() < deadline {
                        if !pid_alive(pid) {
                            return;
                        }
                        std::thread::sleep(std::time::Duration::from_millis(50));
                    }
                    // Already attempted kill; nothing harsher to try via the
                    // shell plugin. Log and move on.
                    crate::log_warn!("[launcher] sidecar still alive after {grace:?}");
                }
            }
            BackendOwnership::Attached
            | BackendOwnership::Unavailable
            | BackendOwnership::StoppedByUser => {}
        }
    }

    fn terminate_raw(child: &mut std::process::Child, grace: std::time::Duration) {
        #[cfg(unix)]
        {
            let pid = child.id() as i32;
            unsafe { libc::kill(pid, libc::SIGTERM) };
            let deadline = std::time::Instant::now() + grace;
            while std::time::Instant::now() < deadline {
                match child.try_wait() {
                    Ok(Some(_)) => return,
                    Ok(None) => std::thread::sleep(std::time::Duration::from_millis(50)),
                    Err(_) => break,
                }
            }
            let _ = child.kill();
            let _ = child.wait();
        }
        #[cfg(windows)]
        {
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

fn pid_alive(pid: u32) -> bool {
    #[cfg(unix)]
    unsafe {
        libc::kill(pid as i32, 0) == 0
    }
    #[cfg(windows)]
    {
        // Best-effort: open the process; if it can't be opened, assume gone.
        use std::os::raw::c_void;
        extern "system" {
            fn OpenProcess(da: u32, ih: i32, pid: u32) -> *mut c_void;
            fn CloseHandle(h: *mut c_void) -> i32;
        }
        const PROCESS_QUERY_LIMITED_INFORMATION: u32 = 0x1000;
        let h = unsafe { OpenProcess(PROCESS_QUERY_LIMITED_INFORMATION, 0, pid) };
        if h.is_null() {
            return false;
        }
        unsafe { CloseHandle(h) };
        true
    }
}

/// Spawn the bundled `rl-toolkit` sidecar. Returns the running child wrapped
/// in `BackendOwnership::SpawnedSidecar`. Stdout/stderr are piped to the
/// launcher's log file at `log_path`.
pub fn spawn_sidecar(
    app: &AppHandle,
    log_path: std::path::PathBuf,
    plugins_dir: Option<String>,
    data_dir: Option<String>,
    rl_addr: Option<String>,
) -> Result<BackendOwnership, String> {
    use std::io::Write;
    let mut cmd = app
        .shell()
        .sidecar("rl-toolkit")
        .map_err(|e| format!("locate sidecar: {e}"))?;

    // Resolve unset dirs against the OS-standard application data dir
    // (see crate::paths) so the sidecar's state lives in the same place
    // regardless of where the launcher was started from. The launcher's
    // own settings file is already there; data and plugins join it.
    // The backend's exe-relative default would otherwise point inside
    // the launcher's bundle, which is read-only on packaged installs.
    let plugins = plugins_dir
        .filter(|p| !p.trim().is_empty())
        .unwrap_or_else(|| {
            crate::paths::default_plugins_dir()
                .to_string_lossy()
                .into_owned()
        });
    let data = data_dir
        .filter(|p| !p.trim().is_empty())
        .unwrap_or_else(|| {
            crate::paths::default_data_dir()
                .to_string_lossy()
                .into_owned()
        });
    cmd = cmd.args([
        "-plugins".to_string(),
        plugins,
        "-data".to_string(),
        data,
    ]);
    if let Some(addr) = rl_addr.filter(|a| !a.trim().is_empty()) {
        cmd = cmd.args(["-rl-addr".to_string(), addr]);
    }

    let (mut rx, child) = cmd.spawn().map_err(|e| format!("spawn sidecar: {e}"))?;

    if let Some(parent) = log_path.parent() {
        let _ = std::fs::create_dir_all(parent);
    }
    let log_file = std::fs::OpenOptions::new()
        .create(true)
        .append(true)
        .open(&log_path)
        .ok();

    tauri::async_runtime::spawn(async move {
        let mut log = log_file;
        while let Some(event) = rx.recv().await {
            let line: Option<Vec<u8>> = match event {
                CommandEvent::Stdout(b) => Some(b),
                CommandEvent::Stderr(b) => Some(b),
                CommandEvent::Terminated(_) => break,
                _ => None,
            };
            if let (Some(buf), Some(f)) = (line, log.as_mut()) {
                let _ = f.write_all(&buf);
                let _ = f.write_all(b"\n");
            }
        }
    });

    Ok(BackendOwnership::SpawnedSidecar(child))
}
