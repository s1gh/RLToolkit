//! Wayland portal backend for the global shortcut.
//!
//! Talks to org.freedesktop.portal.GlobalShortcuts via ashpd. Flow:
//!   1. Create the portal proxy.
//!   2. Create a session (a stable handle the portal uses to scope
//!      our shortcuts).
//!   3. Bind a single shortcut `toggle-edit` with preferred trigger
//!      `CTRL+SHIFT+e`. The compositor shows a consent dialog on
//!      first bind; the user confirms or picks a different trigger.
//!   4. Subscribe to the `Activated` signal stream; for each event
//!      whose shortcut_id is "toggle-edit", call edit_mode::probe_toggle.
//!
//! The session must outlive the listener task: we move both into a
//! tokio task that runs for the process lifetime. There's exactly one
//! shortcut and one session per process; cleanup happens implicitly
//! on process exit when DBus drops the connection.
//!
//! On compositors that don't implement the portal (sway, niri, river),
//! GlobalShortcuts::new or bind_shortcuts will return an error. We log
//! WARN once and exit the task; the tray menu and --toggle-edit CLI
//! remain functional.

use ashpd::desktop::global_shortcuts::{
    BindShortcutsOptions, GlobalShortcuts, NewShortcut,
};
use ashpd::desktop::CreateSessionOptions;
use futures_util::StreamExt;
use tauri::{AppHandle, Runtime};

const SHORTCUT_ID: &str = "toggle-edit";
const SHORTCUT_TRIGGER: &str = "CTRL+SHIFT+e";
const SHORTCUT_DESCRIPTION: &str = "Toggle RL Toolkit overlay edit mode";

pub fn register_portal<R: Runtime>(app: &AppHandle<R>) {
    let app = app.clone();
    tauri::async_runtime::spawn(async move {
        if let Err(e) = run_portal(app).await {
            crate::log_warn!(
                "[launcher] global shortcut on Wayland: portal unavailable: {e} \
                 (use the tray menu or run `rl-widget --toggle-edit`)"
            );
        }
    });
}

async fn run_portal<R: Runtime>(app: AppHandle<R>) -> Result<(), ashpd::Error> {
    let portal = GlobalShortcuts::new().await?;
    let session = portal
        .create_session(CreateSessionOptions::default())
        .await?;

    let new_shortcut = NewShortcut::new(SHORTCUT_ID, SHORTCUT_DESCRIPTION)
        .preferred_trigger(Some(SHORTCUT_TRIGGER));

    // First-time bind shows the compositor's consent dialog. The
    // Request resolves once the user has confirmed (or it can fail if
    // they cancel). Subsequent runs reuse the remembered binding
    // without prompting.
    let bind_request = portal
        .bind_shortcuts(&session, &[new_shortcut], None, BindShortcutsOptions::default())
        .await?;
    let bound = bind_request.response()?;
    let trigger = bound
        .shortcuts()
        .first()
        .map(|s| s.trigger_description().to_string())
        .unwrap_or_else(|| "<none>".to_string());
    crate::log_info!(
        "[launcher] global shortcut bound via portal: trigger={trigger:?} ({} shortcut(s))",
        bound.shortcuts().len()
    );

    let mut activated = portal.receive_activated().await?;
    while let Some(event) = activated.next().await {
        if event.shortcut_id() == SHORTCUT_ID {
            crate::launcher::edit_mode::probe_toggle(&app);
        }
    }
    crate::log_warn!(
        "[launcher] global shortcut portal stream ended; shortcut no longer active \
         (restart the launcher to re-register)"
    );
    Ok(())
}
