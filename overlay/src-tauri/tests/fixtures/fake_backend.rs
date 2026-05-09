//! No-op backend used by `tests/backend_lifecycle.rs`. Listens on the port
//! supplied via `--port` and answers `/api/status` with a toolkit-shaped
//! envelope. Stays running until SIGTERM (Unix) or kill (Windows).

use std::io::{Read, Write};
use std::net::TcpListener;

fn main() {
    let mut port = 0u16;
    let args: Vec<String> = std::env::args().collect();
    for w in args.windows(2) {
        if w[0] == "--port" {
            port = w[1].parse().unwrap_or(0);
        }
    }
    let listener = TcpListener::bind(("127.0.0.1", port)).expect("bind");
    let bound = listener.local_addr().unwrap();
    eprintln!("fake_backend listening on {}", bound);
    for stream in listener.incoming() {
        let mut s = match stream {
            Ok(s) => s,
            Err(_) => continue,
        };
        let mut buf = [0u8; 1024];
        let _ = s.read(&mut buf);
        let body = b"{\"rl_api\":\"connected\"}";
        let _ = write!(
            s,
            "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n",
            body.len()
        );
        let _ = s.write_all(body);
    }
}
