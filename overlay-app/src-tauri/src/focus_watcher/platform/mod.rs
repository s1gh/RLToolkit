//! Per-OS foreground-window query. Stub: returns None on every call. Real
//! implementations land in later tasks.

use super::ForegroundInfo;

pub fn query_foreground() -> Option<ForegroundInfo> {
    None
}
