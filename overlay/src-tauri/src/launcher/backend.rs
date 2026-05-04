//! Backend lifecycle: probe, sidecar spawn, graceful + forceful terminate.
//!
//! The launcher uses `probe_status` to decide whether to attach to an
//! already-running backend or spawn its own sidecar. The validating
//! struct mirrors the existing `/api/status` shape from
//! `backend/server.go:344` — `{"rl_api": <RLStatus>}`. Any deserialization
//! failure means "something else is on this port."

use serde::Deserialize;
use std::time::Duration;

#[derive(Debug, PartialEq, Eq)]
pub enum ProbeOutcome {
    /// 200 + JSON shape we recognize as the toolkit.
    Toolkit,
    /// 200 (or non-error) but the body is not the toolkit's shape.
    Unrelated,
    /// Connection refused, timeout, or other transport error.
    Unreachable,
}

#[derive(Deserialize)]
struct StatusEnvelope {
    #[allow(dead_code)]
    rl_api: serde_json::Value,
}

pub fn probe_status(url: &str, timeout: Duration) -> ProbeOutcome {
    let client = match reqwest::blocking::Client::builder()
        .timeout(timeout)
        .build()
    {
        Ok(c) => c,
        Err(_) => return ProbeOutcome::Unreachable,
    };

    let resp = match client.get(url).send() {
        Ok(r) => r,
        Err(_) => return ProbeOutcome::Unreachable,
    };

    if !resp.status().is_success() {
        return ProbeOutcome::Unrelated;
    }

    match resp.json::<StatusEnvelope>() {
        Ok(_) => ProbeOutcome::Toolkit,
        Err(_) => ProbeOutcome::Unrelated,
    }
}
