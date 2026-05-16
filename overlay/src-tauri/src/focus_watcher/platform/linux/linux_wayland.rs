//! Linux/Wayland foreground-window query via the wlr-foreign-toplevel
//! protocol. Supported by wlroots, KWin, and Hyprland; NOT supported
//! by GNOME/Mutter — on those compositors the registry never advertises
//! the manager interface and we degrade to "always None" (overlay
//! stays visible).

use crate::focus_watcher::{ForegroundInfo, MatchRule};
use std::os::fd::AsRawFd;
use std::sync::{
    atomic::{AtomicBool, Ordering},
    Mutex, OnceLock,
};
use std::time::Duration;
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

/// Block until the compositor sends us an event or `timeout` elapses.
/// Returns immediately if there are events already buffered. The run
/// loop calls this in place of `thread::sleep(POLL_INTERVAL)` so a
/// focus change wakes the watcher within ~1ms of the compositor
/// dispatching the activated/title/closed event, instead of waiting
/// for the next 100ms tick. `timeout` is a hard cap so the debounce
/// state machine still gets a chance to step even when the compositor
/// is silent (e.g. the PendingHide → Inactive transition needs to fire
/// after HIDE_DEBOUNCE with no further events).
///
/// Behaviour notes:
/// - Returns Ok(()) on event, timeout, or signal — all "loop should
///   make a pass now" cases. The caller already tolerates spurious
///   wakes (it just runs query_foreground + step again).
/// - If state init failed earlier (compositor doesn't advertise the
///   foreign-toplevel manager) this falls back to sleeping for the
///   timeout, matching the always-None query behaviour.
pub fn wait_for_event(timeout: Duration) {
    let Some(m) = state() else {
        std::thread::sleep(timeout);
        return;
    };
    let Ok(mut s) = m.lock() else {
        std::thread::sleep(timeout);
        return;
    };

    // Drain anything already buffered first. dispatch_pending may also
    // produce more outgoing requests (e.g. acks); flush after.
    {
        let WaylandState { queue, app_data, conn } = &mut *s;
        let _ = conn.flush();
        if let Err(e) = queue.dispatch_pending(app_data) {
            log_dispatch_error(&e);
        }
        let _ = conn.flush();
    }

    // prepare_read returns None if there are still un-dispatched events
    // in the local queue — in that case there's nothing to wait for,
    // the caller should loop and process them. We held the lock across
    // both the dispatch_pending above and the prepare_read here, so no
    // other thread can race events in between (only this watcher thread
    // ever touches the Wayland connection).
    let raw_fd = {
        let guard = match s.conn.prepare_read() {
            Some(g) => g,
            None => return,
        };
        guard.connection_fd().as_raw_fd()
    };

    // Drop the lock before blocking on poll. Holding it while blocking
    // would deadlock query_foreground (called immediately after this
    // returns from the run loop) — they both want the same Mutex.
    drop(s);

    // poll(2) with POLLIN on the Wayland fd. Timeout is in milliseconds;
    // -1 would block forever, but we always have a finite cap from the
    // caller. Saturating cast covers the (unrealistic) >24-day case.
    let ms = i32::try_from(timeout.as_millis()).unwrap_or(i32::MAX);
    let mut pfd = libc::pollfd {
        fd: raw_fd,
        events: libc::POLLIN,
        revents: 0,
    };
    // Safety: pfd is a valid &mut, count is 1, timeout is an i32 ms
    // value. poll's return value is ignored — Ok(()) covers event,
    // timeout, and EINTR equally.
    unsafe {
        libc::poll(&mut pfd as *mut libc::pollfd, 1, ms);
    }
}

/// Pump pending Wayland events into `app_data` and prune closed
/// toplevels. Shared by query_foreground and query_match so they see
/// the same view of compositor state without each driving the event
/// queue independently.
fn pump_events(s: &mut WaylandState) {
    let WaylandState { queue, app_data, conn } = s;

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

pub fn query_foreground() -> Option<ForegroundInfo> {
    let m = state()?;
    let mut s = m.lock().ok()?;
    pump_events(&mut s);

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

/// Wayland-specific match query.
///
/// `query_foreground`'s strict "find the activated toplevel and apply
/// the rule" path silently fails on Hyprland for the exact case the
/// watcher exists to handle: Rocket League runs under Proton as an
/// XWayland fullscreen client, and Hyprland's wlr-foreign-toplevel
/// manager never reports its `activated` state bit. The toplevel
/// exists with the right title, it just never carries activation.
/// With activated-only the watcher sits in Inactive forever and
/// `hide_when_unfocused` plugins stay opacity:0.
///
/// Decision table — the activated bit IS authoritative when it's set,
/// even when it's set on something other than RL. Treating
/// "RL toplevel exists" as foreground unconditionally regresses the
/// alt-tab case: RL is still in the toplevel list while the user is
/// in their browser, so the overlay would paint over the browser.
///
///   activated matches RL?      → Some(true)
///   activated, doesn't match RL → Some(false)   (alt-tabbed elsewhere)
///   nothing activated, RL exists → Some(true)   (Hyprland XWayland quirk)
///   nothing activated, no RL    → None          (nothing useful to say)
///   no toplevels at all         → None
pub fn query_match(rule: &MatchRule) -> Option<bool> {
    let m = state()?;
    let mut s = m.lock().ok()?;
    pump_events(&mut s);

    let snapshot: Vec<(String, bool)> = s
        .app_data
        .toplevels
        .iter()
        .map(|(_, info)| (info.title.clone(), info.activated))
        .collect();

    if snapshot.is_empty() {
        return None;
    }

    let info_for = |title: &str| ForegroundInfo {
        exe_name: None,
        window_title: Some(title.to_lowercase()),
        pid: 0,
    };

    if let Some((title, _)) = snapshot.iter().find(|(_, activated)| *activated) {
        return Some(rule.apply(&info_for(title)));
    }

    // Nothing activated. If RL's toplevel is present, that's our
    // signal (Hyprland XWayland-fullscreen quirk); otherwise stay
    // quiet so the state machine holds whatever it had.
    if snapshot.iter().any(|(t, _)| rule.apply(&info_for(t))) {
        Some(true)
    } else {
        None
    }
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
