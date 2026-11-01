//! Loopback fake Paperless-ngx for the `paperless migrate` replay. It mirrors
//! the Go fixture server in `cmd/symdesk/paperviews_bodies_process_port_test.go`
//! (`startPVFake`): token check, newest-first document pages with `next`
//! links, per-id downloads and the four entity lists. Every request is
//! recorded as `METHOD request-uri` so the replay can compare the request
//! sequence of the Rust client with the Go client.
#![allow(dead_code)]

use std::{
    collections::BTreeMap,
    io::{Read, Write},
    net::{TcpListener, TcpStream},
    sync::{
        Arc, Mutex,
        atomic::{AtomicBool, Ordering},
    },
    thread,
    time::Duration,
};

use serde::Deserialize;
use serde_json::{Value, json};

#[derive(Deserialize, Clone)]
pub struct FakeFile {
    pub content_type: String,
    #[serde(default)]
    pub disposition: String,
    pub body: String,
}

#[derive(Deserialize, Clone)]
pub struct FakeSpec {
    pub token: String,
    #[serde(default)]
    pub page_size: usize,
    pub documents: Vec<Value>,
    #[serde(default)]
    pub entities: BTreeMap<String, Vec<Value>>,
    #[serde(default)]
    pub files: BTreeMap<String, FakeFile>,
    #[serde(default)]
    pub fail_download: Vec<i64>,
}

pub struct PaperlessFake {
    pub url: String,
    requests: Arc<Mutex<Vec<String>>>,
    stop: Arc<AtomicBool>,
    worker: Option<thread::JoinHandle<()>>,
}

impl PaperlessFake {
    pub fn start(spec: FakeSpec) -> Self {
        let listener = TcpListener::bind("127.0.0.1:0").expect("bind fake paperless");
        listener.set_nonblocking(true).expect("nonblocking listener");
        let address = listener.local_addr().expect("local address");
        let url = format!("http://{address}");
        let requests = Arc::new(Mutex::new(Vec::new()));
        let stop = Arc::new(AtomicBool::new(false));
        let (thread_stop, thread_requests) = (Arc::clone(&stop), Arc::clone(&requests));
        let spec = Arc::new(spec);
        let worker = thread::spawn(move || {
            while !thread_stop.load(Ordering::Relaxed) {
                match listener.accept() {
                    Ok((stream, _)) => {
                        // Accepted sockets inherit the listener's non-blocking mode.
                        stream.set_nonblocking(false).expect("blocking stream");
                        let (spec, requests) = (Arc::clone(&spec), Arc::clone(&thread_requests));
                        thread::spawn(move || serve(stream, &spec, &requests, address.to_string()));
                    }
                    Err(error) if error.kind() == std::io::ErrorKind::WouldBlock => {
                        thread::sleep(Duration::from_millis(2));
                    }
                    Err(error) => panic!("accept fake paperless request: {error}"),
                }
            }
        });
        Self {
            url,
            requests,
            stop,
            worker: Some(worker),
        }
    }

    /// The requests served since the last call.
    pub fn take(&self) -> Vec<String> {
        std::mem::take(&mut *self.requests.lock().expect("request log"))
    }
}

impl Drop for PaperlessFake {
    fn drop(&mut self) {
        self.stop.store(true, Ordering::Relaxed);
        if let Some(worker) = self.worker.take() {
            let _ = worker.join();
        }
    }
}

/// Go `url.QueryEscape`.
fn query_escape(value: &str) -> String {
    let mut out = String::new();
    for byte in value.bytes() {
        match byte {
            b'A'..=b'Z' | b'a'..=b'z' | b'0'..=b'9' | b'-' | b'_' | b'.' | b'~' => {
                out.push(char::from(byte));
            }
            b' ' => out.push('+'),
            other => out.push_str(&format!("%{other:02X}")),
        }
    }
    out
}

fn query_unescape(value: &str) -> String {
    let bytes = value.as_bytes();
    let mut out = Vec::new();
    let mut at = 0;
    while at < bytes.len() {
        match bytes[at] {
            b'+' => out.push(b' '),
            b'%' if at + 2 < bytes.len() => {
                let hex = std::str::from_utf8(&bytes[at + 1..at + 3]).unwrap_or("");
                if let Ok(byte) = u8::from_str_radix(hex, 16) {
                    out.push(byte);
                    at += 2;
                } else {
                    out.push(b'%');
                }
            }
            other => out.push(other),
        }
        at += 1;
    }
    String::from_utf8_lossy(&out).into_owned()
}

fn respond(stream: &mut TcpStream, status: &str, headers: &[(&str, &str)], body: &[u8]) {
    let mut head = format!("HTTP/1.1 {status}\r\nContent-Length: {}\r\nConnection: close\r\n", body.len());
    for (name, value) in headers {
        head.push_str(&format!("{name}: {value}\r\n"));
    }
    head.push_str("\r\n");
    let _ = stream.write_all(head.as_bytes());
    let _ = stream.write_all(body);
    let _ = stream.flush();
}

fn respond_json(stream: &mut TcpStream, value: &Value) {
    let mut body = serde_json::to_vec(value).expect("encode json");
    body.push(b'\n');
    respond(stream, "200 OK", &[("Content-Type", "application/json")], &body);
}

fn serve(mut stream: TcpStream, spec: &FakeSpec, requests: &Mutex<Vec<String>>, host: String) {
    let _ = stream.set_read_timeout(Some(Duration::from_secs(5)));
    let mut buffer = Vec::new();
    let mut chunk = [0_u8; 1024];
    while !buffer.windows(4).any(|window| window == b"\r\n\r\n") {
        match stream.read(&mut chunk) {
            Ok(0) | Err(_) => return,
            Ok(read) => buffer.extend_from_slice(&chunk[..read]),
        }
    }
    let text = String::from_utf8_lossy(&buffer).into_owned();
    let mut lines = text.split("\r\n");
    let request_line = lines.next().unwrap_or_default();
    let mut parts = request_line.split(' ');
    let (method, target) = (
        parts.next().unwrap_or_default().to_owned(),
        parts.next().unwrap_or_default().to_owned(),
    );
    let authorization = lines
        .take_while(|line| !line.is_empty())
        .find_map(|line| {
            let (name, value) = line.split_once(':')?;
            name.eq_ignore_ascii_case("authorization")
                .then(|| value.trim().to_owned())
        })
        .unwrap_or_default();
    requests
        .lock()
        .expect("request log")
        .push(format!("{method} {target}"));
    if authorization != format!("Token {}", spec.token) {
        respond(
            &mut stream,
            "403 Forbidden",
            &[("Content-Type", "application/json")],
            br#"{"detail":"Invalid token."}"#,
        );
        return;
    }
    let (path, raw_query) = target.split_once('?').unwrap_or((target.as_str(), ""));
    let mut query: BTreeMap<String, Vec<String>> = BTreeMap::new();
    for pair in raw_query.split('&').filter(|pair| !pair.is_empty()) {
        let (key, value) = pair.split_once('=').unwrap_or((pair, ""));
        query
            .entry(query_unescape(key))
            .or_default()
            .push(query_unescape(value));
    }
    let first = |name: &str| query.get(name).and_then(|values| values.first()).cloned();
    if path == "/api/documents/" {
        let since = first("created__date__gte").unwrap_or_default();
        let docs: Vec<&Value> = spec
            .documents
            .iter()
            .filter(|doc| {
                since.is_empty()
                    || doc["created_date"].as_str().unwrap_or_default() >= since.as_str()
            })
            .collect();
        let mut size = if spec.page_size == 0 { 100 } else { spec.page_size };
        if let Some(value) = first("page_size").and_then(|v| v.parse::<usize>().ok())
            && value > 0
            && value < size
        {
            size = value;
        }
        let page = first("page")
            .and_then(|v| v.parse::<usize>().ok())
            .filter(|page| *page > 0)
            .unwrap_or(1);
        let start = ((page - 1) * size).min(docs.len());
        let end = (start + size).min(docs.len());
        let next = if end < docs.len() {
            let mut values = query.clone();
            values.insert("page".to_owned(), vec![(page + 1).to_string()]);
            let encoded: Vec<String> = values
                .iter()
                .flat_map(|(key, list)| {
                    list.iter()
                        .map(move |value| format!("{}={}", query_escape(key), query_escape(value)))
                })
                .collect();
            Value::String(format!("http://{host}/api/documents/?{}", encoded.join("&")))
        } else {
            Value::Null
        };
        respond_json(
            &mut stream,
            &json!({"count": docs.len(), "next": next, "previous": null, "results": &docs[start..end]}),
        );
    } else if let Some(id) = path
        .strip_prefix("/api/documents/")
        .and_then(|rest| rest.strip_suffix("/download/"))
    {
        if spec
            .fail_download
            .iter()
            .any(|failing| failing.to_string() == id)
        {
            respond(
                &mut stream,
                "500 Internal Server Error",
                &[("Content-Type", "text/plain; charset=utf-8"), ("X-Content-Type-Options", "nosniff")],
                b"boom\n",
            );
            return;
        }
        match spec.files.get(id) {
            Some(file) => {
                let mut headers = vec![("Content-Type", file.content_type.as_str())];
                if !file.disposition.is_empty() {
                    headers.push(("Content-Disposition", file.disposition.as_str()));
                }
                respond(&mut stream, "200 OK", &headers, file.body.as_bytes());
            }
            None => respond(
                &mut stream,
                "404 Not Found",
                &[("Content-Type", "text/plain; charset=utf-8")],
                b"404 page not found\n",
            ),
        }
    } else if path.starts_with("/api/") && path.matches('/').count() == 3 {
        let kind = path.trim_start_matches("/api/").trim_matches('/');
        let results = spec.entities.get(kind).cloned().unwrap_or_default();
        respond_json(
            &mut stream,
            &json!({"count": results.len(), "next": null, "previous": null, "results": results}),
        );
    } else {
        respond(
            &mut stream,
            "404 Not Found",
            &[("Content-Type", "text/plain; charset=utf-8")],
            b"404 page not found\n",
        );
    }
}
