//! End-to-end probe + spawn + terminate, using `fake_backend` as a stand-in
//! for the real Go backend. Pure orchestration test — no Tauri runtime.

use rl_widget::launcher::backend::{probe_status, BackendOwnership, ProbeOutcome};
use std::process::{Command, Stdio};
use std::time::Duration;

fn fake_backend_path() -> std::path::PathBuf {
    // Cargo places integration-test binaries' siblings — including auxiliary
    // [[bin]] entries — alongside the test executable's parent's parent.
    let test_exe = std::env::current_exe().unwrap();
    let mut dir = test_exe.parent().unwrap().to_path_buf();
    if dir.ends_with("deps") {
        dir.pop();
    }
    let exe = if cfg!(windows) { "fake_backend.exe" } else { "fake_backend" };
    dir.join(exe)
}

fn free_port() -> u16 {
    let l = std::net::TcpListener::bind("127.0.0.1:0").unwrap();
    let port = l.local_addr().unwrap().port();
    drop(l);
    port
}

#[test]
fn probe_attaches_to_running_backend() {
    let port = free_port();
    let mut child = Command::new(fake_backend_path())
        .args(["--port", &port.to_string()])
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .spawn()
        .expect("spawn fake_backend");

    // Wait briefly for it to bind.
    std::thread::sleep(Duration::from_millis(150));

    let url = format!("http://127.0.0.1:{}/api/status", port);
    let outcome = probe_status(&url, Duration::from_millis(500));

    let _ = child.kill();
    let _ = child.wait();

    assert_eq!(outcome, ProbeOutcome::Toolkit);
}

#[test]
fn ownership_spawned_terminate_kills_child() {
    let port = free_port();
    let child = Command::new(fake_backend_path())
        .args(["--port", &port.to_string()])
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .spawn()
        .expect("spawn fake_backend");
    let pid = child.id();
    let mut owned = BackendOwnership::Spawned(child);
    owned.terminate(Duration::from_secs(2));

    // After terminate the child must have exited.
    #[cfg(unix)]
    unsafe {
        // kill -0 returns -1/ESRCH if no such process.
        let alive = libc::kill(pid as i32, 0) == 0;
        assert!(!alive, "child {} should be dead", pid);
    }
    #[cfg(windows)]
    {
        let _ = pid; // best-effort: rely on terminate having waited
    }
}
