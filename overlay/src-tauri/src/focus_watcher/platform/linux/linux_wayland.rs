//! Linux/Wayland foreground-window query via the wlr-foreign-toplevel
//! protocol. Supported by wlroots, KWin, and Hyprland; NOT supported
//! by GNOME/Mutter — on those compositors the registry never advertises
//! the manager interface and we degrade to "always None" (overlay
//! stays visible).

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

    // Initial roundtrip pulls existing-window events synchronously;
    // without it, the first few polls see an empty toplevel list.
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
    // Non-blocking poll pattern: flush outgoing → dispatch buffered →
    // prepare_read + read (WouldBlock = no events this tick) →
    // dispatch what we just read. dispatch_pending alone won't pull
    // events off the socket. The borrow is split so the checker sees
    // disjoint fields.
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

    // The manager's `toplevel` event (opcode 0) creates a child
    // ZwlrForeignToplevelHandleV1; this macro tells wayland-client
    // which Dispatch impl to attach to it. Without this, it panics
    // with "Missing event_created_child specialization for opcode 0".
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
                // State is a u32 array in host byte order; "activated" is enum 2.
                info.activated = bytes
                    .chunks_exact(4)
                    .map(|c| u32::from_ne_bytes([c[0], c[1], c[2], c[3]]))
                    .any(|v| v == 2);
            }
            HandleEvent::Closed => {
                info.closed = true;
            }
            _ => {}
        }
    }
}
