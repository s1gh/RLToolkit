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
        quit_hotkey: None,
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
    // After a save the temp sidecar file (.launcher.json.tmp) must not
    // remain on disk. The save path either renamed it onto launcher.json
    // or returned an error.
    let dir = TempDir::new().unwrap();
    let store = SettingsStore::new(dir.path().join("launcher.json"));
    store.save(&LauncherSettings::default()).unwrap();
    let leftover = dir.path().join(".launcher.json.tmp");
    assert!(!leftover.exists(), "temp file should not survive save");
}
