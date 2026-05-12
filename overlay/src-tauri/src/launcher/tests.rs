use crate::launcher::settings::{LauncherSettings, SettingsStore};
use tempfile::TempDir;

#[test]
fn defaults_when_file_missing() {
    let dir = TempDir::new().unwrap();
    let store = SettingsStore::new(dir.path().join("launcher.json"));
    let s = store.load();
    assert!(!s.overlay_enabled);
    assert_eq!(s.window_w, None);
    assert_eq!(s.window_h, None);
}

#[test]
fn round_trip_writes_and_reads() {
    let dir = TempDir::new().unwrap();
    let store = SettingsStore::new(dir.path().join("launcher.json"));
    let s = LauncherSettings {
        overlay_enabled: true,
        window_x: Some(100),
        window_y: Some(200),
        window_w: Some(720),
        window_h: Some(640),
        backend_port: None,
        ..LauncherSettings::default()
    };
    store.save(&s).unwrap();
    let loaded = store.load();
    assert!(loaded.overlay_enabled);
    assert_eq!(loaded.window_w, Some(720));
}

#[test]
fn corrupt_file_falls_back_to_defaults() {
    let dir = TempDir::new().unwrap();
    let path = dir.path().join("launcher.json");
    std::fs::write(&path, "{ this is not valid json").unwrap();
    let store = SettingsStore::new(path);
    let s = store.load();
    assert!(!s.overlay_enabled);
}

#[test]
fn save_is_atomic_via_tempfile_rename() {
    // The .launcher.json.tmp sidecar must not survive a successful
    // save — it should have been renamed onto launcher.json.
    let dir = TempDir::new().unwrap();
    let store = SettingsStore::new(dir.path().join("launcher.json"));
    store.save(&LauncherSettings::default()).unwrap();
    let leftover = dir.path().join(".launcher.json.tmp");
    assert!(!leftover.exists(), "temp file should not survive save");
}

use crate::launcher::backend::{probe_status, ProbeOutcome};

fn spawn_test_server(body: &'static [u8], status: u16) -> (String, std::thread::JoinHandle<()>) {
    use std::io::{Read, Write};
    use std::net::TcpListener;
    let listener = TcpListener::bind("127.0.0.1:0").unwrap();
    let addr = listener.local_addr().unwrap();
    let handle = std::thread::spawn(move || {
        for stream in listener.incoming() {
            let mut s = match stream {
                Ok(s) => s,
                Err(_) => return,
            };
            let mut buf = [0u8; 1024];
            let _ = s.read(&mut buf);
            let resp = format!(
                "HTTP/1.1 {} OK\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n",
                status,
                body.len()
            );
            let _ = s.write_all(resp.as_bytes());
            let _ = s.write_all(body);
            return;
        }
    });
    (format!("http://{}/api/status", addr), handle)
}

#[test]
fn probe_returns_attached_for_toolkit_response() {
    // /api/status ships rl_api as a Status string ("connected" /
    // "disconnected"); see backend/internal/server/server.go.
    let (url, _h) = spawn_test_server(b"{\"rl_api\":\"connected\"}", 200);
    match probe_status(&url, std::time::Duration::from_millis(500)) {
        ProbeOutcome::Toolkit { rl_api } => assert_eq!(rl_api, "connected"),
        other => panic!("expected Toolkit, got {:?}", other),
    }
}

#[test]
fn probe_returns_unrelated_for_non_toolkit_response() {
    let (url, _h) = spawn_test_server(b"{\"hello\":\"world\"}", 200);
    match probe_status(&url, std::time::Duration::from_millis(500)) {
        ProbeOutcome::Unrelated => {}
        other => panic!("expected Unrelated, got {:?}", other),
    }
}

#[test]
fn probe_returns_unreachable_when_nothing_listening() {
    // Bind and immediately drop to get a port nobody's on.
    let l = std::net::TcpListener::bind("127.0.0.1:0").unwrap();
    let port = l.local_addr().unwrap().port();
    drop(l);
    let url = format!("http://127.0.0.1:{}/api/status", port);
    match probe_status(&url, std::time::Duration::from_millis(500)) {
        ProbeOutcome::Unreachable => {}
        other => panic!("expected Unreachable, got {:?}", other),
    }
}

use crate::launcher::backend::BackendOwnership;

#[test]
fn ownership_attached_does_not_kill_on_terminate() {
    let mut owned = BackendOwnership::Attached;
    // terminate is a no-op on Attached.
    owned.terminate(std::time::Duration::from_millis(10));
    assert!(matches!(owned, BackendOwnership::Attached));
}

#[test]
fn ownership_unavailable_terminate_is_noop() {
    let mut owned = BackendOwnership::Unavailable;
    owned.terminate(std::time::Duration::from_millis(10));
    assert!(matches!(owned, BackendOwnership::Unavailable));
}

use crate::launcher::edit_mode::EditModeState;
use std::sync::atomic::Ordering;

#[test]
fn edit_mode_state_default_is_inactive() {
    let s = EditModeState::<tauri::Wry>::default();
    assert!(!s.is_active());
}

#[test]
fn edit_mode_state_active_flag_round_trips() {
    let s = EditModeState::<tauri::Wry>::default();
    s.active_for_test().store(true, Ordering::SeqCst);
    assert!(s.is_active());
    s.active_for_test().store(false, Ordering::SeqCst);
    assert!(!s.is_active());
}
