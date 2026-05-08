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

fn log_dispatch_error(e: &impl std::fmt::Display) {
    log_dispatch_error_str(&e.to_string());
}

fn log_dispatch_error_str(msg: &str) {
    if !DISPATCH_ERR_LOGGED.swap(true, Ordering::Relaxed) {
        crate::log_warn!(
            "[rl-widget] wayland dispatch failed (compositor restart?): {msg}; \
             focus-gating effectively disabled until process restart"
        );
    }
}

fn try_init() -> Result<WaylandState, String> {
    let conn = Connection::connect_to_env().map_err(|e| format!("connect: {e}"))?;
    let (globals, mut queue) = registry_queue_init::<AppData>(&conn).map_err(|e| e.to_string())?;
    let qh = queue.handle();

    // Bind the foreign-toplevel manager, if advertised.
    let _manager = globals
        .bind::<ZwlrForeignToplevelManagerV1, _, _>(&qh, 1..=3, ())
        .map_err(|e| format!("bind foreign-toplevel-manager: {e}"))?;

    // Pull the initial burst of Toplevel/Title/State events for windows
    // that already exist. Without this, the first few polls would see an
    // empty toplevel list while the compositor's reply is still en route.
    let mut app_data = AppData::default();
    queue
        .roundtrip(&mut app_data)
        .map_err(|e| format!("initial roundtrip: {e}"))?;

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
                crate::log_info!("[rl-widget] wayland foreign-toplevel-manager bound; \
                           focus-gating active");
                Some(Mutex::new(s))
            }
            Err(e) => {
                crate::log_warn!("[rl-widget] focus-gating disabled: wayland compositor \
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
    // Pull events off the wayland socket and dispatch them, then prune
    // closed toplevels.
    //
    // dispatch_pending alone is NOT enough: it only processes events
    // already in the queue's internal buffer. Nothing puts them there
    // unless we explicitly read from the socket via prepare_read +
    // ReadEventsGuard::read(). The canonical non-blocking-poll pattern:
    //
    //   1. flush our outgoing requests
    //   2. dispatch anything already buffered
    //   3. if prepare_read returns Some(guard), call guard.read()
    //      — WouldBlock means "no events ready this tick"; any other
    //      Err is a real socket failure
    //   4. dispatch what we just read
    //
    // Split the borrow explicitly so the borrow-checker sees disjoint fields.
    {
        let WaylandState { queue, app_data, conn } = &mut *s;

        let _ = conn.flush();

        if let Err(e) = queue.dispatch_pending(app_data) {
            log_dispatch_error(&e);
        }

        if let Some(guard) = conn.prepare_read() {
            match guard.read() {
                Ok(_) => {
                    if let Err(e) = queue.dispatch_pending(app_data) {
                        log_dispatch_error(&e);
                    }
                }
                Err(wayland_client::backend::WaylandError::Io(io_err))
                    if io_err.kind() == std::io::ErrorKind::WouldBlock =>
                {
                    // No events ready this tick — normal idle path.
                }
                Err(e) => log_dispatch_error_str(&format!("read: {e}")),
            }
        }

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

    // The manager's `toplevel` event (opcode 0) creates a new
    // ZwlrForeignToplevelHandleV1 object. wayland-client needs to know
    // which Dispatch impl to attach to that child; without this override,
    // it panics with "Missing event_created_child specialization for
    // event opcode 0". The user_data is `()` since we look up state by
    // proxy identity in the Handle dispatch impl.
    wayland_client::event_created_child!(
        AppData,
        ZwlrForeignToplevelManagerV1,
        [0 => (ZwlrForeignToplevelHandleV1, ())]
    );
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
