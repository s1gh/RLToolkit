//! Overlay live edit mode.
//!
//! Holds an in-memory active flag, the pre-activation overlay
//! visibility snapshot, and the orchestration that flips the
//! overlay window's click-through bit and the JS hook that drives
//! the page's drag wrappers.
//!
//! Four activation entry points (global shortcut, tray menu, CLI
//! --toggle-edit, dashboard button in Phase 3) funnel through
//! `toggle`. Atomic compare_exchange on the active flag prevents two
//! near-simultaneous activations from both proceeding.

use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Mutex;
use tauri::menu::MenuItem;
use tauri::{AppHandle, Manager, Runtime, WebviewWindow};

pub struct EditModeState<R: Runtime> {
    active: AtomicBool,
    pre_edit_visible: Mutex<Option<bool>>,
    // Stored at tray setup time so update_tray_label can reach it
    // without needing a menu getter that Tauri 2 doesn't expose.
    pub(crate) tray_menu_item: Mutex<Option<MenuItem<R>>>,
}

impl<R: Runtime> Default for EditModeState<R> {
    fn default() -> Self {
        Self {
            active: AtomicBool::new(false),
            pre_edit_visible: Mutex::new(None),
            tray_menu_item: Mutex::new(None),
        }
    }
}

impl<R: Runtime> EditModeState<R> {
    pub fn is_active(&self) -> bool {
        self.active.load(Ordering::SeqCst)
    }
}

/// Toggle the state. Returns the new value, or Err if activation
/// could not be completed (eval failure, missing overlay window).
pub fn toggle<R: Runtime>(app: &AppHandle<R>) -> Result<bool, String> {
    let state = app
        .try_state::<EditModeState<R>>()
        .ok_or_else(|| "edit_mode state not registered".to_string())?;
    let current = state.active.load(Ordering::SeqCst);
    set(app, !current)
}

/// Set the state to a specific value. Idempotent.
pub fn set<R: Runtime>(app: &AppHandle<R>, on: bool) -> Result<bool, String> {
    let state = app
        .try_state::<EditModeState<R>>()
        .ok_or_else(|| "edit_mode state not registered".to_string())?;
    let current = state.active.load(Ordering::SeqCst);
    if current == on {
        return Ok(current);
    }
    if state
        .active
        .compare_exchange(current, on, Ordering::SeqCst, Ordering::SeqCst)
        .is_err()
    {
        // Lost the race; the winner is doing the work. We report what
        // they last wrote, but note that they may still be mid-flight
        // and could roll back to the prior value if they fail. Callers
        // only log on Err, so the worst case is a stale-looking log
        // line, not a state corruption.
        return Ok(state.active.load(Ordering::SeqCst));
    }

    let window = match app.get_webview_window("main") {
        Some(w) => w,
        None => {
            state.active.store(false, Ordering::SeqCst);
            return Err("overlay window not built".to_string());
        }
    };

    if on {
        let was_visible = window.is_visible().unwrap_or(false);
        *state.pre_edit_visible.lock().unwrap() = Some(was_visible);
        if !was_visible {
            let _ = window.show();
            // Drive the aggregator's plugin remount path explicitly.
            // The previous hide() ran __rlt_prepare_for_hide which
            // unmounts every iframe; visibilitychange would normally
            // remount via applyConfig(lastMerged) on the next show,
            // but Tauri's window.show() doesn't reliably flip
            // document.visibilityState on WebView2 — the overlay
            // comes back empty. Calling the explicit hook here keeps
            // edit-mode-after-hide working. See overlay.html for the
            // long version.
            let _ = window.eval(
                "if (typeof window.__rlt_resume_after_show === 'function') \
                 { window.__rlt_resume_after_show(); }",
            );
        }
        if let Err(e) = set_clickthrough(&window, false) {
            // Roll back: hide if we showed, clear state.
            if !was_visible {
                let _ = window.hide();
            }
            state.active.store(false, Ordering::SeqCst);
            *state.pre_edit_visible.lock().unwrap() = None;
            return Err(format!("disable click-through: {e}"));
        }
        if let Err(e) =
            window.eval("window.__rlt_set_live_edit && window.__rlt_set_live_edit(true);")
        {
            // Roll back the click-through and visibility changes.
            let _ = set_clickthrough(&window, true);
            if !was_visible {
                let _ = window.hide();
            }
            state.active.store(false, Ordering::SeqCst);
            *state.pre_edit_visible.lock().unwrap() = None;
            return Err(format!("eval set_live_edit(true): {e}"));
        }
        crate::log_info!("[overlay-edit] activated");
    } else {
        // Tear-down order: drop the JS chrome first so a final stray
        // click during teardown lands on a wrapper, not the game,
        // then restore click-through, then maybe re-hide. eval errors
        // are ignored here: restoring click-through matters more than
        // the JS hook, and the JS state is recreated on next activation
        // anyway.
        let _ =
            window.eval("window.__rlt_set_live_edit && window.__rlt_set_live_edit(false);");
        let _ = set_clickthrough(&window, true);
        let pre = state.pre_edit_visible.lock().unwrap().take();
        if let Some(false) = pre {
            let _ = window.hide();
        }
        crate::log_info!("[overlay-edit] deactivated");
    }

    update_tray_label(app, on);
    Ok(on)
}

/// Toggle the overlay's click-through state.
///
/// `clickthrough=true` makes clicks pass through to the game beneath;
/// `clickthrough=false` captures them on the overlay window (so the
/// JS drag wrappers can receive pointer events).
///
/// Linux is special. The overlay window is a wlr-layer-shell surface
/// whose click-through is implemented via the GTK input shape (an empty
/// cairo region installed at init time, see init_layer_shell_common in
/// main.rs). Tauri's set_ignore_cursor_events doesn't route through
/// gtk_window.input_shape_combine_region on layer-shell, so calling it
/// either no-ops or leaves the surface in a half-configured state that
/// can hang the compositor on the next focus change. We bypass Tauri
/// and toggle the input shape directly.
///
/// On Windows and macOS we use Tauri's native call, which maps to
/// WS_EX_TRANSPARENT and ignoresMouseEvents respectively.
fn set_clickthrough<R: Runtime>(
    window: &WebviewWindow<R>,
    clickthrough: bool,
) -> Result<(), String> {
    #[cfg(target_os = "linux")]
    {
        use gtk::prelude::WidgetExt;
        let gtk_window = window
            .gtk_window()
            .map_err(|e| format!("gtk_window unavailable: {e}"))?;
        if clickthrough {
            // Empty region: no interactive area, compositor routes
            // input beneath.
            let region = gtk::cairo::Region::create();
            gtk_window.input_shape_combine_region(Some(&region));
        } else {
            // None clears the shape, so the whole surface accepts input.
            gtk_window.input_shape_combine_region(None);
        }
        return Ok(());
    }

    #[cfg(not(target_os = "linux"))]
    {
        window
            .set_ignore_cursor_events(clickthrough)
            .map_err(|e| format!("set_ignore_cursor_events({clickthrough}): {e}"))?;

        #[cfg(target_os = "windows")]
        {
            reassert_layered(window);
            force_nc_redraw(window);
        }

        Ok(())
    }
}

/// Re-assert WS_EX_LAYERED on the overlay HWND after a click-through
/// toggle. Tauri's set_ignore_cursor_events(false) on Windows clears
/// WS_EX_LAYERED along with WS_EX_TRANSPARENT, which drops the window
/// out of layered composition. The HWND's background brush then paints
/// the unmapped portion of the client area (the strip above where
/// WebView2 actually draws), producing a white bar at the top of the
/// screen the moment edit mode activates.
///
/// Fix: re-set WS_EX_LAYERED and reapply per-pixel alpha. Best-effort;
/// failures are silent because this is a paint cleanup, not a
/// correctness invariant.
#[cfg(target_os = "windows")]
fn reassert_layered<R: Runtime>(window: &WebviewWindow<R>) {
    use windows_sys::Win32::Foundation::HWND;
    use windows_sys::Win32::UI::WindowsAndMessaging::{
        GetWindowLongPtrW, SetLayeredWindowAttributes, SetWindowLongPtrW, GWL_EXSTYLE, LWA_ALPHA,
        WS_EX_LAYERED,
    };

    let Ok(hwnd) = window.hwnd() else { return };
    let hwnd: HWND = hwnd.0;

    unsafe {
        let ex = GetWindowLongPtrW(hwnd, GWL_EXSTYLE);
        if ex == 0 {
            return;
        }
        let new_ex = ex | (WS_EX_LAYERED as isize);
        if new_ex != ex {
            SetWindowLongPtrW(hwnd, GWL_EXSTYLE, new_ex);
        }
        // LWA_ALPHA with bAlpha=255 keeps the window fully opaque at
        // the layered-attribute level so the WebView2 per-pixel alpha
        // (via .transparent(true)) remains the source of truth.
        let _ = SetLayeredWindowAttributes(hwnd, 0, 255, LWA_ALPHA);
    }
}

/// Force a non-client repaint of the overlay HWND. Independent of any
/// style toggle, DWM caches a stale NC frame containing the window's
/// title text and renders it as a ghost bar across the top of the
/// screen the next time the window is touched (focus change, click
/// elsewhere, edit-mode flip). Invalidating the frame with
/// RDW_FRAME | RDW_INVALIDATE | RDW_UPDATENOW makes DWM rebuild the
/// cache against the current style (no caption, layered, transparent)
/// and the ghost bar disappears. Confirmed via live PowerShell tests
/// that RedrawWindow alone clears the bar whether or not the window
/// region is also touched.
#[cfg(target_os = "windows")]
fn force_nc_redraw<R: Runtime>(window: &WebviewWindow<R>) {
    use windows_sys::Win32::Foundation::HWND;
    use windows_sys::Win32::Graphics::Gdi::{
        RedrawWindow, RDW_FRAME, RDW_INVALIDATE, RDW_UPDATENOW,
    };

    let Ok(hwnd) = window.hwnd() else { return };
    let hwnd: HWND = hwnd.0;

    unsafe {
        let _ = RedrawWindow(
            hwnd,
            std::ptr::null(),
            std::ptr::null_mut(),
            RDW_FRAME | RDW_INVALIDATE | RDW_UPDATENOW,
        );
    }
}

fn update_tray_label<R: Runtime>(app: &AppHandle<R>, on: bool) {
    let Some(state) = app.try_state::<EditModeState<R>>() else {
        return;
    };
    let label = if on {
        "Exit overlay edit mode"
    } else {
        "Edit overlay layout"
    };
    let guard = state.tray_menu_item.lock().unwrap();
    if let Some(item) = guard.as_ref() {
        let _ = item.set_text(label);
    }
}

#[cfg(test)]
impl<R: Runtime> EditModeState<R> {
    pub(crate) fn active_for_test(&self) -> &AtomicBool {
        &self.active
    }
}
