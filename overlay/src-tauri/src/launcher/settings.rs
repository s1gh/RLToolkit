//! Persistent launcher settings stored at `data/launcher.json`.
//!
//! Writes go through a temp-file + rename so a crash mid-write can never
//! leave a partial file on disk. Geometry writes are debounced upstream
//! (see `launcher::window`); this module performs the I/O unconditionally.

use serde::{Deserialize, Serialize};
use std::fs;
use std::io::Write;
use std::path::{Path, PathBuf};

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
#[serde(default)]
pub struct LauncherSettings {
    pub overlay_enabled: bool,
    pub window_x: Option<i32>,
    pub window_y: Option<i32>,
    pub window_w: Option<i32>,
    pub window_h: Option<i32>,
    pub backend_port: Option<u16>,
    pub quit_hotkey: Option<String>,
    pub plugins_dir: Option<String>,
    pub data_dir: Option<String>,
}

pub struct SettingsStore {
    path: PathBuf,
}

impl SettingsStore {
    pub fn new(path: impl Into<PathBuf>) -> Self {
        Self { path: path.into() }
    }

    /// Load settings from disk. Returns defaults if the file is missing
    /// or unreadable, or if its contents fail to parse — corrupt files
    /// must never block startup.
    pub fn load(&self) -> LauncherSettings {
        let bytes = match fs::read(&self.path) {
            Ok(b) => b,
            Err(_) => return LauncherSettings::default(),
        };
        serde_json::from_slice(&bytes).unwrap_or_default()
    }

    pub fn save(&self, s: &LauncherSettings) -> std::io::Result<()> {
        if let Some(parent) = self.path.parent() {
            fs::create_dir_all(parent)?;
        }
        let tmp = self.tmp_path();
        let bytes = serde_json::to_vec_pretty(s)
            .map_err(|e| std::io::Error::new(std::io::ErrorKind::InvalidData, e))?;
        {
            let mut f = fs::File::create(&tmp)?;
            f.write_all(&bytes)?;
            f.sync_all()?;
        }
        let result = fs::rename(&tmp, &self.path);
        // If the rename failed, clean up the tmp file so it doesn't pile up.
        if result.is_err() {
            let _ = fs::remove_file(&tmp);
        }
        result
    }

    fn tmp_path(&self) -> PathBuf {
        let mut name = ".".to_string();
        if let Some(stem) = self.path.file_name().and_then(|n| n.to_str()) {
            name.push_str(stem);
        } else {
            name.push_str("launcher.json");
        }
        name.push_str(".tmp");
        let mut p = self
            .path
            .parent()
            .map(Path::to_path_buf)
            .unwrap_or_default();
        p.push(name);
        p
    }
}
