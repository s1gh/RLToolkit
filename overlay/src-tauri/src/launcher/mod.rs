//! Launcher mode for rl-widget. Owns the Go backend's process lifecycle,
//! shows a control-panel window embedding the toolkit dashboard, and
//! coexists with the existing transparent overlay window.

pub mod backend;
pub mod settings;

#[cfg(test)]
mod tests;
