//! Loopback fake LLM provider shared by the configured-provider replays. It
//! serves scripted turns (Ollama `/api/chat`, Ollama `/api/generate`, Anthropic
//! `/v1/messages`), records what the CLI sent, and can stall a turn open so a
//! test can cancel the in-flight call and observe the disconnect. Nothing here
//! leaves the loopback interface.
#![allow(dead_code)]

use std::{
    io::{Read, Write},
    net::{Shutdown, TcpListener, TcpStream},
    sync::{
        Arc, Mutex,
        atomic::{AtomicBool, AtomicUsize, Ordering},
    },
    thread,
    time::{Duration, Instant},
};

use serde::Deserialize;

/// The generated fake API key (never a real credential).
pub const KEY: &str = "fake-key-0123456789abcdef";
pub const HANDLE_TOKEN: &str = "{{HANDLE}}";

#[derive(Clone, Deserialize)]
pub struct Turn {
    #[serde(default)]
    pub status: u16,
    pub body: String,
    #[serde(default)]
    pub stall: bool,
    /// Hold the response back (milliseconds) before anything is written.
    #[serde(default)]
    pub delay_ms: u64,
}

#[derive(Clone, Debug, Default)]
pub struct Seen {
    pub method: String,
    pub path: String,
    pub headers: Vec<(String, String)>,
    pub body: Vec<u8>,
}

struct Inner {
    turns: Vec<Turn>,
    seen: Mutex<Vec<Seen>>,
    last_chat: Mutex<String>,
    served: AtomicUsize,
    stop: AtomicBool,
    started: AtomicBool,
    disconnected: AtomicBool,
}

pub struct FakeProvider {
    pub url: String,
    inner: Arc<Inner>,
}

impl FakeProvider {
    pub fn start(turns: Vec<Turn>) -> Self {
        let listener = TcpListener::bind("127.0.0.1:0").expect("bind loopback provider");
        listener.set_nonblocking(true).unwrap();
        let url = format!("http://{}", listener.local_addr().unwrap());
        let inner = Arc::new(Inner {
            turns,
            seen: Mutex::default(),
            last_chat: Mutex::default(),
            served: AtomicUsize::new(0),
            stop: AtomicBool::new(false),
            started: AtomicBool::new(false),
            disconnected: AtomicBool::new(false),
        });
        let accept = inner.clone();
        thread::spawn(move || {
            while !accept.stop.load(Ordering::SeqCst) {
                match listener.accept() {
                    Ok((stream, _)) => {
                        // macOS hands out accepted sockets in non-blocking mode.
                        stream.set_nonblocking(false).ok();
                        let inner = accept.clone();
                        thread::spawn(move || inner.serve(stream));
                    }
                    Err(error) if error.kind() == std::io::ErrorKind::WouldBlock => {
                        thread::sleep(Duration::from_millis(2));
                    }
                    Err(_) => break,
                }
            }
        });
        Self { url, inner }
    }

    pub fn requests(&self) -> Vec<Seen> {
        self.inner.seen.lock().unwrap().clone()
    }

    /// A chunk of a stalled turn reached the client.
    pub fn wait_started(&self, limit: Duration) -> bool {
        wait(&self.inner.started, limit)
    }

    /// The client closed the connection of a stalled turn.
    pub fn wait_disconnected(&self, limit: Duration) -> bool {
        wait(&self.inner.disconnected, limit)
    }
}

fn wait(flag: &AtomicBool, limit: Duration) -> bool {
    let deadline = Instant::now() + limit;
    while Instant::now() < deadline {
        if flag.load(Ordering::SeqCst) {
            return true;
        }
        thread::sleep(Duration::from_millis(10));
    }
    flag.load(Ordering::SeqCst)
}

impl Drop for FakeProvider {
    fn drop(&mut self) {
        self.inner.stop.store(true, Ordering::SeqCst);
    }
}

impl Inner {
    fn serve(&self, mut stream: TcpStream) {
        stream.set_read_timeout(Some(Duration::from_secs(10))).ok();
        let mut raw = Vec::new();
        let mut chunk = [0_u8; 8192];
        let header_end = loop {
            if let Some(at) = raw.windows(4).position(|window| window == b"\r\n\r\n") {
                break at + 4;
            }
            match stream.read(&mut chunk) {
                Ok(0) | Err(_) => return,
                Ok(n) => raw.extend_from_slice(&chunk[..n]),
            }
        };
        let head = String::from_utf8_lossy(&raw[..header_end]).into_owned();
        let mut lines = head.split("\r\n");
        let request_line = lines.next().unwrap_or_default();
        let mut parts = request_line.split(' ');
        let method = parts.next().unwrap_or_default().to_owned();
        let path = parts.next().unwrap_or_default().to_owned();
        let headers: Vec<(String, String)> = lines
            .filter_map(|line| line.split_once(':'))
            .map(|(name, value)| (name.trim().to_lowercase(), value.trim().to_owned()))
            .collect();
        let length = headers
            .iter()
            .find(|(name, _)| name == "content-length")
            .and_then(|(_, value)| value.parse::<usize>().ok())
            .unwrap_or(0);
        let chunked = headers
            .iter()
            .any(|(name, value)| name == "transfer-encoding" && value.contains("chunked"));
        let mut body = raw[header_end..].to_vec();
        if chunked {
            while !body.ends_with(b"0\r\n\r\n") {
                match stream.read(&mut chunk) {
                    Ok(0) | Err(_) => break,
                    Ok(n) => body.extend_from_slice(&chunk[..n]),
                }
            }
            body = decode_chunked(&body);
        } else {
            while body.len() < length {
                match stream.read(&mut chunk) {
                    Ok(0) | Err(_) => break,
                    Ok(n) => body.extend_from_slice(&chunk[..n]),
                }
            }
        }
        let chat = matches!(
            path.as_str(),
            "/api/chat" | "/api/generate" | "/v1/messages"
        );
        let body_text = String::from_utf8_lossy(&body).into_owned();
        self.seen.lock().unwrap().push(Seen {
            method,
            path: path.clone(),
            headers,
            body: body.clone(),
        });
        // The handle a tool result announced is part of this very request.
        let previous = {
            let mut last = self.last_chat.lock().unwrap();
            if chat {
                *last = body_text;
            }
            last.clone()
        };
        if !chat {
            // Go's `http.NotFound`, which the Go fake provider serves.
            let _ = stream.write_all(
                b"HTTP/1.1 404 Not Found\r\ncontent-type: text/plain; charset=utf-8\r\ncontent-length: 19\r\nconnection: close\r\n\r\n404 page not found\n",
            );
            return;
        }
        let index = self.served.fetch_add(1, Ordering::SeqCst);
        let Some(turn) = self.turns.get(index) else {
            let message = br#"{"error":"no scripted turn"}"#;
            let _ = stream.write_all(
                format!(
                    "HTTP/1.1 500 Internal Server Error\r\ncontent-length: {}\r\nconnection: close\r\n\r\n",
                    message.len()
                )
                .as_bytes(),
            );
            let _ = stream.write_all(message);
            return;
        };
        if turn.delay_ms > 0 {
            thread::sleep(Duration::from_millis(turn.delay_ms));
        }
        let mut reply = turn.body.clone();
        if reply.contains(HANDLE_TOKEN) {
            if let Some(handle) = externalized_handle(&previous) {
                reply = reply.replace(HANDLE_TOKEN, &handle);
            }
        }
        let status = if turn.status == 0 { 200 } else { turn.status };
        let content_type = if path.starts_with("/v1/") && status == 200 {
            "text/event-stream"
        } else {
            "application/json"
        };
        if turn.stall {
            // Chunked so the first chunk reaches the client while the rest is
            // withheld.
            let head = format!(
                "HTTP/1.1 {status} OK\r\ncontent-type: {content_type}\r\ntransfer-encoding: chunked\r\nconnection: close\r\n\r\n"
            );
            let first = format!("{:x}\r\n{reply}\r\n", reply.len());
            if stream.write_all(head.as_bytes()).is_err()
                || stream.write_all(first.as_bytes()).is_err()
                || stream.flush().is_err()
            {
                self.disconnected.store(true, Ordering::SeqCst);
                return;
            }
            self.started.store(true, Ordering::SeqCst);
            // Stalled provider: wait for the client to leave.
            stream
                .set_read_timeout(Some(Duration::from_millis(50)))
                .ok();
            let deadline = Instant::now() + Duration::from_secs(30);
            let mut sink = [0_u8; 64];
            while Instant::now() < deadline {
                match stream.read(&mut sink) {
                    Ok(0) => {
                        self.disconnected.store(true, Ordering::SeqCst);
                        return;
                    }
                    Ok(_) => {}
                    Err(error)
                        if matches!(
                            error.kind(),
                            std::io::ErrorKind::WouldBlock | std::io::ErrorKind::TimedOut
                        ) => {}
                    Err(_) => {
                        self.disconnected.store(true, Ordering::SeqCst);
                        return;
                    }
                }
            }
            return;
        }
        let head = format!(
            "HTTP/1.1 {status} OK\r\ncontent-type: {content_type}\r\ncontent-length: {}\r\nconnection: close\r\n\r\n",
            reply.len()
        );
        let _ = stream.write_all(head.as_bytes());
        let _ = stream.write_all(reply.as_bytes());
        let _ = stream.flush();
        self.started.store(true, Ordering::SeqCst);
        let _ = stream.shutdown(Shutdown::Both);
    }
}

fn decode_chunked(raw: &[u8]) -> Vec<u8> {
    let mut out = Vec::new();
    let mut rest = raw;
    loop {
        let Some(line_end) = rest.windows(2).position(|window| window == b"\r\n") else {
            return out;
        };
        let size = usize::from_str_radix(
            std::str::from_utf8(&rest[..line_end]).unwrap_or("0").trim(),
            16,
        )
        .unwrap_or(0);
        if size == 0 {
            return out;
        }
        let start = line_end + 2;
        if rest.len() < start + size {
            return out;
        }
        out.extend_from_slice(&rest[start..start + size]);
        rest = &rest[(start + size + 2).min(rest.len())..];
    }
}

/// The handle the externalizer announced in the previous request, e.g.
/// `task-123/desk_ls-001.txt`.
fn externalized_handle(request: &str) -> Option<String> {
    let at = request.find("externalized to task-")? + "externalized to ".len();
    let rest = &request[at..];
    let end = rest.find(".txt")? + ".txt".len();
    Some(rest[..end].to_owned())
}
