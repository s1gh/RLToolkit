//! Windows foreground-window query.
//!
//! Path: GetForegroundWindow → GetWindowThreadProcessId →
//! OpenProcess(QueryLimitedInformation) → QueryFullProcessImageNameW.
//! We also pull GetWindowTextW for the title field, which the matcher
//! ignores on Windows (ExeEquals strategy) but is useful for stderr
//! diagnostics.

use crate::focus_watcher::ForegroundInfo;
use windows_sys::Win32::Foundation::{CloseHandle, HWND, MAX_PATH};
use windows_sys::Win32::System::Threading::{
    OpenProcess, QueryFullProcessImageNameW, PROCESS_NAME_WIN32, PROCESS_QUERY_LIMITED_INFORMATION,
};
use windows_sys::Win32::UI::WindowsAndMessaging::{
    GetForegroundWindow, GetWindowTextW, GetWindowThreadProcessId,
};

pub fn query_foreground() -> Option<ForegroundInfo> {
    // SAFETY: GetForegroundWindow returns a HWND that's valid for at most
    // a snapshot moment; we use it before any await/yield. A null return
    // means no window has focus — return None and let the watcher retry.
    let hwnd: HWND = unsafe { GetForegroundWindow() };
    if hwnd.is_null() {
        return None;
    }

    let mut pid: u32 = 0;
    // SAFETY: hwnd is non-null per the check above. The function writes
    // the owning PID into our stack u32.
    unsafe { GetWindowThreadProcessId(hwnd, &mut pid) };
    if pid == 0 {
        return None;
    }

    let exe_name = read_exe_basename(pid);
    let window_title = read_window_title(hwnd);

    Some(ForegroundInfo {
        exe_name: exe_name.map(|s| s.to_ascii_lowercase()),
        window_title: window_title.map(|s| s.to_ascii_lowercase()),
        pid,
    })
}

fn read_exe_basename(pid: u32) -> Option<String> {
    // SAFETY: OpenProcess returns null on failure (insufficient access,
    // process gone). We check before use and CloseHandle on success.
    let handle =
        unsafe { OpenProcess(PROCESS_QUERY_LIMITED_INFORMATION, 0, pid) };
    if handle.is_null() {
        return None;
    }

    let mut buf = vec![0u16; MAX_PATH as usize];
    let mut len: u32 = buf.len() as u32;
    // SAFETY: handle is non-null; we pass a writable buffer and its length
    // (in WCHARs). On success, len is updated to the WCHAR count written.
    let ok = unsafe {
        QueryFullProcessImageNameW(handle, PROCESS_NAME_WIN32, buf.as_mut_ptr(), &mut len)
    };
    // SAFETY: handle is owned here; CloseHandle is the documented free.
    unsafe { CloseHandle(handle) };

    if ok == 0 {
        return None;
    }

    let path = String::from_utf16_lossy(&buf[..len as usize]);
    // basename: strip everything up to the last backslash or forward slash.
    let basename = path
        .rsplit(|c| c == '\\' || c == '/')
        .next()?
        .to_string();
    if basename.is_empty() {
        None
    } else {
        Some(basename)
    }
}

fn read_window_title(hwnd: HWND) -> Option<String> {
    let mut buf = vec![0u16; 512];
    // SAFETY: hwnd validity is the caller's responsibility (checked above);
    // GetWindowTextW writes at most buf.len() WCHARs and null-terminates.
    let n = unsafe { GetWindowTextW(hwnd, buf.as_mut_ptr(), buf.len() as i32) };
    if n <= 0 {
        return None;
    }
    Some(String::from_utf16_lossy(&buf[..n as usize]))
}
