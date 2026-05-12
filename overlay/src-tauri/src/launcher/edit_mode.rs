//! Overlay live edit mode.
//!
//! Phase 1: probe-only. Three entry points (global shortcut, tray
//! item, CLI --toggle-edit) all call probe_toggle, which logs and
//! briefly flashes the tray tooltip so we can visually confirm the
//! shortcut fires on each platform.
//!
//! Phases 2 and 3 replace probe_toggle with the real toggle that
//! drives the overlay window's click-through bit and a JS module on
//! the loaded overlay page.

use std::sync::atomic::{AtomicBool, AtomicU64, Ordering};
use std::sync::{Mutex, OnceLock};
use std::time::Duration;
use tauri::{AppHandle, Runtime};

/// In-memory flag flipped by every probe call. Visible via the tray
/// tooltip flash. Lives outside Tauri-managed state so the probe is
/// usable from the single-instance callback before app state is set up.
static PROBE_STATE: AtomicBool = AtomicBool::new(false);

/// Original tray tooltip captured before the first flash so we can
/// restore it after each probe pulse.
static ORIGINAL_TOOLTIP: OnceLock<Mutex<Option<String>>> = OnceLock::new();

/// Bumped every press. Restore threads capture the generation at
/// spawn and only restore if it's still current when they wake — this
/// stops a stale restore from clobbering a freshly-flashed tooltip
/// when the user mashes the shortcut.
static RESTORE_GENERATION: AtomicU64 = AtomicU64::new(0);

/// Phase 1 probe. Flips PROBE_STATE, logs the new value, flashes the
/// tray tooltip for ~2 seconds.
pub fn probe_toggle<R: Runtime>(app: &AppHandle<R>) {
    let was_active = PROBE_STATE.fetch_xor(true, Ordering::SeqCst);
    let new_active = !was_active;
    crate::log_info!(
        "[overlay-edit] shortcut fired (active={})",
        new_active
    );
    flash_tray_tooltip(app, new_active);
}

fn flash_tray_tooltip<R: Runtime>(app: &AppHandle<R>, new_active: bool) {
    let tray = match app.tray_by_id("main") {
        Some(t) => t,
        None => return,
    };
    capture_original_tooltip();
    let message = if new_active {
        "Edit mode toggled ON (probe)"
    } else {
        "Edit mode toggled OFF (probe)"
    };
    let _ = tray.set_tooltip(Some(message));
    let generation = RESTORE_GENERATION.fetch_add(1, Ordering::SeqCst) + 1;
    schedule_restore(app, generation);
}

fn capture_original_tooltip() {
    let slot = ORIGINAL_TOOLTIP.get_or_init(|| Mutex::new(None));
    let mut guard = slot.lock().unwrap();
    if guard.is_some() {
        return;
    }
    // Tauri's TrayIcon doesn't expose a get_tooltip. We know the value
    // we set at startup ("RL Toolkit") so we hardcode the restore
    // target; if a future refactor changes the startup tooltip, update
    // this string too.
    *guard = Some("RL Toolkit".to_string());
}

fn schedule_restore<R: Runtime + 'static>(app: &AppHandle<R>, generation: u64) {
    let app = app.clone();
    std::thread::spawn(move || {
        std::thread::sleep(Duration::from_millis(2000));
        // Skip restore if another press has happened since we were spawned.
        if RESTORE_GENERATION.load(Ordering::SeqCst) != generation {
            return;
        }
        let tray = match app.tray_by_id("main") {
            Some(t) => t,
            None => return,
        };
        let slot = ORIGINAL_TOOLTIP.get_or_init(|| Mutex::new(None));
        let original = slot.lock().unwrap().clone().unwrap_or_default();
        let _ = tray.set_tooltip(Some(original.as_str()));
    });
}
