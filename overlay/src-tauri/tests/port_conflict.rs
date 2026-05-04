//! Verifies the launcher's probe surfaces "port in use by something else"
//! cleanly. Spins up a stub HTTP server on a random port that returns
//! 200 + non-toolkit JSON, and asserts the probe returns Unrelated.

use rl_widget::launcher::backend::{probe_status, ProbeOutcome};
use std::io::{Read, Write};
use std::net::TcpListener;
use std::time::Duration;

#[test]
fn probe_reports_unrelated_when_other_server_owns_port() {
    let listener = TcpListener::bind("127.0.0.1:0").unwrap();
    let addr = listener.local_addr().unwrap();
    std::thread::spawn(move || {
        for stream in listener.incoming() {
            let mut s = match stream { Ok(s) => s, Err(_) => continue };
            let mut buf = [0u8; 1024]; let _ = s.read(&mut buf);
            let body = b"{\"hello\":\"not toolkit\"}";
            let _ = write!(s, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n", body.len());
            let _ = s.write_all(body);
        }
    });

    let url = format!("http://{}/api/status", addr);
    let outcome = probe_status(&url, Duration::from_millis(500));
    assert_eq!(outcome, ProbeOutcome::Unrelated);
}
