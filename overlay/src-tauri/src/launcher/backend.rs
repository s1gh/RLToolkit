//! Backend lifecycle: probe, sidecar spawn, graceful + forceful terminate.
//!
//! `probe_status` decides whether to attach to an already-running
//! backend or spawn a sidecar. It mirrors `/api/status`'s shape
//! (`{"rl_api": <RLStatus>}`); any deserialization failure means
//! "something else is on this port."

use serde::Deserialize;
use std::path::PathBuf;
use std::sync::OnceLock;
use std::time::Duration;

/// True when the named env var is set to a recognized truthy value
/// ("1", "true", "yes", "on"). Anything else is false.
fn env_truthy(name: &str) -> bool {
    matches!(
        std::env::var(name)
            .unwrap_or_default()
            .trim()
            .to_ascii_lowercase()
            .as_str(),
        "1" | "true" | "yes" | "on"
    )
}

/// Path to today's sidecar capture log. Safety net for stdout/stderr
/// that bypasses `log.Printf` (Go runtime panics, raw `fmt.Print`,
/// stack traces); the Go backend's own structured log is at
/// `backend-YYYY-MM-DD.log`.
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
    // Reuse a single client across polls. Building a fresh
    // reqwest::blocking::Client every 2s allocates a tokio runtime, a
    // connection pool, and re-parses the rustls config — pointless for
    // a localhost loop. The per-request timeout is applied below so the
    // client's own timeout is left unset.
    static CLIENT: OnceLock<reqwest::blocking::Client> = OnceLock::new();
    let client = CLIENT.get_or_init(|| {
        reqwest::blocking::Client::builder()
            .build()
            .expect("build reqwest client")
    });

    let resp = match client.get(url).timeout(timeout).send() {
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

/// Tracks who started the running backend. SpawnedSidecar is the
/// production path (tauri_plugin_shell child); SpawnedRaw is for
/// integration tests with a fake binary. Attached / Unavailable
/// no-op on terminate. StoppedByUser is distinct from Unavailable
/// so the UI can show "Start backend" rather than "Backend crashed".
#[derive(Debug)]
pub enum BackendOwnership {
    SpawnedSidecar(CommandChild),
    SpawnedRaw(std::process::Child),
    Attached,
    Unavailable,
    StoppedByUser,
}

impl BackendOwnership {
    pub fn from_raw(c: std::process::Child) -> Self {
        // Mirror the sidecar path: attach to the launcher's Job
        // Object on Windows so test fakes also get cleaned up if the
        // test binary is killed mid-run. No-op elsewhere.
        crate::launcher::job_object::attach(c.id());
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
                // CommandChild::kill consumes self; swap the variant
                // out so we can take ownership.
                let owned = std::mem::replace(self, BackendOwnership::Unavailable);
                if let BackendOwnership::SpawnedSidecar(child) = owned {
                    let pid = child.pid();
                    let deadline = std::time::Instant::now() + grace;
                    let _ = child.kill();
                    while std::time::Instant::now() < deadline {
                        if !pid_alive(pid) {
                            return;
                        }
                        std::thread::sleep(std::time::Duration::from_millis(50));
                    }
                    // Already kill()ed; nothing harsher available via shell plugin.
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
        // If the process can't be opened, assume it's gone.
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
    // (see crate::paths). The backend's exe-relative default would
    // otherwise point inside the launcher's bundle, which is read-only
    // on packaged installs.
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
    // Plugin hot-reload via `rl-toolkit dev` needs the sidecar's
    // localhost dev API. Off by default; opt in with RLT_DEV=1.
    if env_truthy("RLT_DEV") {
        cmd = cmd.args(["-dev".to_string()]);
    }

    let (mut rx, child) = cmd.spawn().map_err(|e| format!("spawn sidecar: {e}"))?;

    // On Windows, hand the child to the launcher's Job Object so that
    // any death of the launcher (clean or violent) takes the sidecar
    // with it. No-op elsewhere; Linux uses PR_SET_PDEATHSIG inside
    // the backend itself.
    crate::launcher::job_object::attach(child.pid());

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
