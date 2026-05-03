//! macOS foreground-app query.
//!
//! NSWorkspace.sharedWorkspace.frontmostApplication returns the app the
//! user is interacting with. We pull localizedName for the title field;
//! processIdentifier gives us the PID for the self-PID exception. RL
//! doesn't officially run on macOS anymore, so this exists for symmetry
//! and for users running it via CrossOver/Parallels (the wrapper app
//! shows up here, so the title-substring rule needs to match the
//! wrapper's name — that's a user-configuration decision, not ours).

use crate::focus_watcher::ForegroundInfo;
use objc2_app_kit::NSWorkspace;

pub fn query_foreground() -> Option<ForegroundInfo> {
    // SAFETY: NSWorkspace.sharedWorkspace is an Objective-C class method
    // that returns an autoreleased shared instance. frontmostApplication
    // is nil-safe (returns Option<Retained<NSRunningApplication>>).
    let workspace = unsafe { NSWorkspace::sharedWorkspace() };
    let app = unsafe { workspace.frontmostApplication() }?;

    let pid = unsafe { app.processIdentifier() };
    let pid = if pid > 0 { pid as u32 } else { return None };

    let title = unsafe { app.localizedName() }
        .map(|ns| ns.to_string())
        .map(|s| s.to_lowercase());

    Some(ForegroundInfo {
        exe_name: None,
        window_title: title,
        pid,
    })
}
