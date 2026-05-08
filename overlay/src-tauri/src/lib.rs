//! Library re-exports so integration tests can use `rl_widget::launcher::*`.
//! The CLI entry point remains in `src/main.rs`.

pub mod cli;
pub mod focus_watcher;
pub mod launcher;
pub mod logging;
pub mod overlay_bridge;
pub mod paths;
