//! Linux/X11 foreground-window query: _NET_ACTIVE_WINDOW on the root
//! window, then _NET_WM_PID + _NET_WM_NAME on the active window via
//! x11rb (pure-Rust, no libX11 link). Connection state is cached in a
//! OnceLock; an init failure degrades to "always None" for the rest
//! of the process's life.

use crate::focus_watcher::ForegroundInfo;
use std::sync::OnceLock;
use x11rb::connection::Connection;
use x11rb::protocol::xproto::{
    AtomEnum, ConnectionExt, GetPropertyReply, Window,
};
use x11rb::rust_connection::RustConnection;

struct X11State {
    conn: RustConnection,
    root: Window,
    net_active_window: u32,
    net_wm_pid: u32,
    net_wm_name: u32,
    utf8_string: u32,
}

static STATE: OnceLock<Option<X11State>> = OnceLock::new();

fn state() -> Option<&'static X11State> {
    STATE
        .get_or_init(|| match init_state() {
            Ok(s) => Some(s),
            Err(e) => {
                crate::log_warn!("[rl-widget] focus-gating: X11 init failed: {e}; \
                           overlay will remain always-visible");
                None
            }
        })
        .as_ref()
}

fn init_state() -> Result<X11State, String> {
    let (conn, screen_num) = x11rb::connect(None).map_err(|e| e.to_string())?;
    let root = conn.setup().roots[screen_num].root;
    let net_active_window = intern(&conn, b"_NET_ACTIVE_WINDOW")?;
    let net_wm_pid = intern(&conn, b"_NET_WM_PID")?;
    let net_wm_name = intern(&conn, b"_NET_WM_NAME")?;
    let utf8_string = intern(&conn, b"UTF8_STRING")?;
    Ok(X11State {
        conn,
        root,
        net_active_window,
        net_wm_pid,
        net_wm_name,
        utf8_string,
    })
}

fn intern(conn: &RustConnection, name: &[u8]) -> Result<u32, String> {
    let cookie = conn.intern_atom(false, name).map_err(|e| e.to_string())?;
    let reply = cookie.reply().map_err(|e| e.to_string())?;
    Ok(reply.atom)
}

pub fn query_foreground() -> Option<ForegroundInfo> {
    let s = state()?;
    let active = read_active_window(s)?;
    if active == 0 {
        return None;
    }
    let pid = read_pid(s, active).unwrap_or(0);
    let title = read_title(s, active);
    if pid == 0 && title.is_none() {
        return None;
    }
    Some(ForegroundInfo {
        exe_name: None,
        window_title: title.map(|t| t.to_lowercase()),
        pid,
    })
}

fn read_active_window(s: &X11State) -> Option<Window> {
    let cookie = s
        .conn
        .get_property(false, s.root, s.net_active_window, AtomEnum::WINDOW, 0, 1)
        .ok()?;
    let reply = cookie.reply().ok()?;
    if reply.format != 32 || reply.value.len() < 4 {
        return None;
    }
    Some(u32::from_ne_bytes([
        reply.value[0],
        reply.value[1],
        reply.value[2],
        reply.value[3],
    ]))
}

fn read_pid(s: &X11State, win: Window) -> Option<u32> {
    let reply = read_prop(&s.conn, win, s.net_wm_pid, AtomEnum::CARDINAL.into())?;
    if reply.format != 32 || reply.value.len() < 4 {
        return None;
    }
    Some(u32::from_ne_bytes([
        reply.value[0],
        reply.value[1],
        reply.value[2],
        reply.value[3],
    ]))
}

fn read_title(s: &X11State, win: Window) -> Option<String> {
    // Prefer _NET_WM_NAME (UTF-8) over WM_NAME (latin-1).
    if let Some(reply) = read_prop(&s.conn, win, s.net_wm_name, s.utf8_string) {
        if !reply.value.is_empty() {
            return Some(String::from_utf8_lossy(&reply.value).into_owned());
        }
    }
    let reply = read_prop(&s.conn, win, AtomEnum::WM_NAME.into(), AtomEnum::STRING.into())?;
    if reply.value.is_empty() {
        None
    } else {
        Some(String::from_utf8_lossy(&reply.value).into_owned())
    }
}

fn read_prop(
    conn: &RustConnection,
    win: Window,
    prop: u32,
    ty: u32,
) -> Option<GetPropertyReply> {
    let cookie = conn
        .get_property(false, win, prop, ty, 0, u32::MAX / 4)
        .ok()?;
    cookie.reply().ok()
}
