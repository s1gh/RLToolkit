//! Windows Job Object glue. Ensures the rl-toolkit sidecar dies the
//! moment the launcher process dies, even when the launcher exits
//! ungracefully (Task Manager "End Task", taskkill /F, panic, OOM,
//! crash). Without this the sidecar gets reparented and runs forever
//! with no UI to reach it.
//!
//! The pattern: the launcher creates one Job Object at startup and
//! holds the only handle to it. Every spawned sidecar is assigned to
//! the job. The job carries JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE, so
//! when the launcher's handle to the job is closed (which always
//! happens at process exit, however violent), Windows kills every
//! process in the job.
//!
//! No-op on non-Windows platforms (the module is empty there).

#[cfg(target_os = "windows")]
mod imp {
    use std::sync::OnceLock;

    use windows_sys::Win32::Foundation::{CloseHandle, HANDLE, INVALID_HANDLE_VALUE};
    use windows_sys::Win32::System::JobObjects::{
        AssignProcessToJobObject, CreateJobObjectW, SetInformationJobObject,
        JobObjectExtendedLimitInformation, JOBOBJECT_BASIC_LIMIT_INFORMATION,
        JOBOBJECT_EXTENDED_LIMIT_INFORMATION, JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
    };
    use windows_sys::Win32::System::Threading::{OpenProcess, PROCESS_SET_QUOTA, PROCESS_TERMINATE};

    /// Wrapper so we can stash the raw HANDLE in a OnceLock and still
    /// call CloseHandle on it if we ever need to. Send/Sync because a
    /// HANDLE is just an opaque pointer Windows manages internally;
    /// the API is thread-safe for our usage (one-time create, repeat
    /// AssignProcessToJobObject calls).
    struct JobHandle(HANDLE);
    unsafe impl Send for JobHandle {}
    unsafe impl Sync for JobHandle {}

    static JOB: OnceLock<Option<JobHandle>> = OnceLock::new();

    /// Lazily create the launcher's Job Object. Returns the cached
    /// handle on subsequent calls. None means creation failed (logged
    /// once at first attempt) and we'll fall back to the old behavior
    /// where the sidecar can outlive the launcher.
    fn get_or_create() -> Option<HANDLE> {
        JOB.get_or_init(|| unsafe {
            let h = CreateJobObjectW(std::ptr::null(), std::ptr::null());
            if h.is_null() || h == INVALID_HANDLE_VALUE {
                let err = std::io::Error::last_os_error();
                crate::log_warn!("[launcher] CreateJobObject failed: {err}");
                return None;
            }
            // KILL_ON_JOB_CLOSE is the whole point: when the launcher
            // process exits and Windows closes its open handles, the
            // job object closes too and every process inside it gets
            // TerminateProcess'd by the kernel.
            let mut info: JOBOBJECT_EXTENDED_LIMIT_INFORMATION = std::mem::zeroed();
            info.BasicLimitInformation = JOBOBJECT_BASIC_LIMIT_INFORMATION {
                LimitFlags: JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
                ..std::mem::zeroed()
            };
            let ok = SetInformationJobObject(
                h,
                JobObjectExtendedLimitInformation,
                &info as *const _ as *const _,
                std::mem::size_of::<JOBOBJECT_EXTENDED_LIMIT_INFORMATION>() as u32,
            );
            if ok == 0 {
                let err = std::io::Error::last_os_error();
                crate::log_warn!("[launcher] SetInformationJobObject failed: {err}");
                let _ = CloseHandle(h);
                return None;
            }
            Some(JobHandle(h))
        })
        .as_ref()
        .map(|j| j.0)
    }

    /// Add the given process to the launcher's Job Object. Logs and
    /// returns on failure rather than panicking — a missed assignment
    /// just means that one sidecar instance can outlive the launcher,
    /// which matches the pre-job-object behavior.
    pub fn attach(pid: u32) {
        let Some(job) = get_or_create() else { return };
        unsafe {
            // PROCESS_SET_QUOTA + PROCESS_TERMINATE is the documented
            // minimum access for AssignProcessToJobObject.
            let proc = OpenProcess(PROCESS_SET_QUOTA | PROCESS_TERMINATE, 0, pid);
            if proc.is_null() {
                let err = std::io::Error::last_os_error();
                crate::log_warn!("[launcher] OpenProcess({pid}) failed: {err}");
                return;
            }
            let ok = AssignProcessToJobObject(job, proc);
            if ok == 0 {
                let err = std::io::Error::last_os_error();
                crate::log_warn!(
                    "[launcher] AssignProcessToJobObject({pid}) failed: {err}",
                );
            }
            let _ = CloseHandle(proc);
        }
    }
}

#[cfg(target_os = "windows")]
pub use imp::attach;

/// No-op on non-Windows. Linux uses PR_SET_PDEATHSIG inside the
/// backend itself; macOS is not yet covered.
#[cfg(not(target_os = "windows"))]
pub fn attach(_pid: u32) {}
