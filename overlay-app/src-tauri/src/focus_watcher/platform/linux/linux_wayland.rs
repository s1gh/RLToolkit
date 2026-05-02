//! Linux/Wayland foreground-window query.
//!
//! Uses ext-foreign-toplevel-list-v1 via the wlr extension to enumerate
//! toplevels and read the activated state. Supported by wlroots, KWin,
//! and Hyprland. NOT supported by GNOME/Mutter — on those compositors,
//! the global registry never advertises the manager interface; we
//! detect this at startup and degrade to "always None" for the rest of
//! the process's life (overlay stays visible always).
//!
//! State (the registry connection + per-toplevel handles) is held in a
//! Mutex<Option<…>> so query_foreground() can mutate the toplevel list
//! incrementally on each event-pump.

use crate::focus_watcher::ForegroundInfo;
use std::sync::{
    atomic::{AtomicBool, Ordering},
    Mutex, OnceLock,
};
use wayland_client::protocol::wl_registry;
use wayland_client::{
    globals::{registry_queue_init, GlobalListContents},
    Connection, Dispatch, EventQueue, QueueHandle,
};
use wayland_protocols_wlr::foreign_toplevel::v1::client::{
    zwlr_foreign_toplevel_handle_v1::{Event as HandleEvent, ZwlrForeignToplevelHandleV1},
    zwlr_foreign_toplevel_manager_v1::{
        Event as ManagerEvent, ZwlrForeignToplevelManagerV1,
    },
};

#[derive(Default, Clone)]
struct ToplevelInfo {
    title: String,
    activated: bool,
    pid: u32, // wlr-foreign-toplevel doesn't expose PID; left at 0
    closed: bool,
}

struct WaylandState {
    conn: Connection,
    queue: EventQueue<AppData>,
    app_data: AppData,
}

#[derive(Default)]
struct AppData {
    toplevels: Vec<(ZwlrForeignToplevelHandleV1, ToplevelInfo)>,
}

static STATE: OnceLock<Option<Mutex<WaylandState>>> = OnceLock::new();
static DISPATCH_ERR_LOGGED: AtomicBool = AtomicBool::new(false);

fn try_init() -> Result<WaylandState, String> {
    let conn = Connection::connect_to_env().map_err(|e| format!("connect: {e}"))?;
    let (globals, queue) = registry_queue_init::<AppData>(&conn).map_err(|e| e.to_string())?;
    let qh = queue.handle();

    // Bind the foreign-toplevel manager, if advertised.
    let _manager = globals
        .bind::<ZwlrForeignToplevelManagerV1, _, _>(&qh, 1..=3, ())
        .map_err(|e| format!("bind foreign-toplevel-manager: {e}"))?;

    let app_data = AppData::default();
    Ok(WaylandState {
        conn,
        queue,
        app_data,
    })
}

fn state() -> Option<&'static Mutex<WaylandState>> {
    STATE
        .get_or_init(|| match try_init() {
            Ok(s) => {
                eprintln!("[rl-widget] wayland foreign-toplevel-manager bound; \
                           focus-gating active");
                Some(Mutex::new(s))
            }
            Err(e) => {
                eprintln!("[rl-widget] focus-gating disabled: wayland compositor \
                           doesn't expose toplevel focus ({e}); overlay will \
                           remain always-visible");
                None
            }
        })
        .as_ref()
}

pub fn query_foreground() -> Option<ForegroundInfo> {
    let m = state()?;
    let mut s = m.lock().ok()?;
    // Drain any pending events from the compositor (toplevel created /
    // closed / state changed). dispatch_pending reads from the socket.
    // Split the borrow explicitly so the borrow-checker sees disjoint fields.
    {
        let WaylandState { queue, app_data, conn } = &mut *s;
        let dispatch_result = queue.dispatch_pending(app_data);
        if let Err(e) = dispatch_result {
            if !DISPATCH_ERR_LOGGED.swap(true, Ordering::Relaxed) {
                eprintln!("[rl-widget] wayland dispatch failed (compositor restart?): {e}; \
                           focus-gating effectively disabled until process restart");
            }
        }
        let _ = conn.flush();
        app_data.toplevels.retain(|(_, info)| !info.closed);
    }

    let active = s
        .app_data
        .toplevels
        .iter()
        .find(|(_, info)| info.activated)
        .map(|(_, info)| info.clone())?;

    Some(ForegroundInfo {
        exe_name: None,
        window_title: Some(active.title.to_lowercase()),
        pid: active.pid,
    })
}

// --- wayland-client dispatch glue ---

impl Dispatch<wl_registry::WlRegistry, GlobalListContents> for AppData {
    fn event(
        _: &mut Self,
        _: &wl_registry::WlRegistry,
        _: wl_registry::Event,
        _: &GlobalListContents,
        _: &Connection,
        _: &QueueHandle<Self>,
    ) {
        // No-op — we only ever bind during init.
    }
}

impl Dispatch<ZwlrForeignToplevelManagerV1, ()> for AppData {
    fn event(
        state: &mut Self,
        _: &ZwlrForeignToplevelManagerV1,
        event: ManagerEvent,
        _: &(),
        _: &Connection,
        _: &QueueHandle<Self>,
    ) {
        if let ManagerEvent::Toplevel { toplevel } = event {
            state.toplevels.push((toplevel, ToplevelInfo::default()));
        }
    }
}

impl Dispatch<ZwlrForeignToplevelHandleV1, ()> for AppData {
    fn event(
        state: &mut Self,
        proxy: &ZwlrForeignToplevelHandleV1,
        event: HandleEvent,
        _: &(),
        _: &Connection,
        _: &QueueHandle<Self>,
    ) {
        let info = state
            .toplevels
            .iter_mut()
            .find(|(h, _)| h == proxy)
            .map(|(_, i)| i);
        let Some(info) = info else { return };

        match event {
            HandleEvent::Title { title } => info.title = title,
            HandleEvent::State { state: bytes } => {
                // wlr-foreign-toplevel-state is a byte array of u32 enum values
                // (host byte order — wayland-client has already byte-swapped on
                // receive). "activated" is enum variant 2.
                info.activated = bytes
                    .chunks_exact(4)
                    .map(|c| u32::from_ne_bytes([c[0], c[1], c[2], c[3]]))
                    .any(|v| v == 2);
            }
            HandleEvent::Closed => {
                info.closed = true;
                // We also tell the server we're done with this proxy. The actual
                // Vec entry is pruned by query_foreground below.
            }
            _ => {}
        }
    }
}
