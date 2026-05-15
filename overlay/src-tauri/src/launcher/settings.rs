//! Persistent launcher settings stored at `data/launcher.json`. Writes
//! use temp-file + rename so a crash mid-write can't leave a partial
//! file on disk. Geometry writes are debounced upstream (see
//! `launcher::window`).

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
    pub plugins_dir: Option<String>,
    pub data_dir: Option<String>,
    pub rl_addr: Option<String>,
    /// Global shortcut combo for toggling overlay edit mode. Canonical
    /// form follows the global-hotkey parser: modifiers then key,
    /// joined by `+`, e.g. `"Ctrl+Shift+KeyE"`. None means use the
    /// built-in default (Ctrl+Shift+E).
    pub edit_mode_shortcut: Option<String>,
}

pub struct SettingsStore {
    path: PathBuf,
}

impl SettingsStore {
    pub fn new(path: impl Into<PathBuf>) -> Self {
        Self { path: path.into() }
    }

    /// Load settings, returning defaults on missing / unreadable /
    /// unparseable file — startup must never block on corruption.
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
