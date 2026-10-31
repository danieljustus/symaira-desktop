//! Loopback fake Paperless API shared by the Paperless import contracts: scripted
//! routes keyed by path and `page` query, request recording, `Connection:
//! close` responses.
#![allow(dead_code)]

use base64::Engine as _;
use serde_json::{Value, json};
use std::collections::BTreeMap;
use std::io::{BufRead, BufReader, Read, Write};
use std::net::{TcpListener, TcpStream};
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, Mutex};

#[derive(Clone)]
pub struct Route {
    pub path: String,
    pub page: String,
    pub status: u16,
    pub headers: BTreeMap<String, String>,
    pub body: String,
    pub body_b64: String,
}

pub fn routes_from(value: &Value) -> Vec<Route> {
    value
        .as_array()
        .map(|list| {
            list.iter()
                .map(|r| Route {
                    path: r["path"].as_str().unwrap().to_owned(),
                    page: r["page"].as_str().unwrap_or("").to_owned(),
                    status: r["status"].as_u64().unwrap() as u16,
                    headers: r["headers"]
                        .as_object()
                        .map(|m| {
                            m.iter()
                                .map(|(k, v)| (k.clone(), v.as_str().unwrap().to_owned()))
                                .collect()
                        })
                        .unwrap_or_default(),
                    body: r["body"].as_str().unwrap_or("").to_owned(),
                    body_b64: r["body_b64"].as_str().unwrap_or("").to_owned(),
                })
                .collect()
        })
        .unwrap_or_default()
}

fn reason(status: u16) -> &'static str {
    match status {
        200 => "OK",
        401 => "Unauthorized",
        404 => "Not Found",
        500 => "Internal Server Error",
        _ => "Status",
    }
}

pub struct Server {
    pub host: String,
    pub routes: Arc<Mutex<Vec<Route>>>,
    pub requests: Arc<Mutex<Vec<Value>>>,
    stop: Arc<AtomicBool>,
    thread: Option<std::thread::JoinHandle<()>>,
}

impl Server {
    pub fn start(routes: Vec<Route>) -> Self {
        let listener = TcpListener::bind("127.0.0.1:0").unwrap();
        let host = listener.local_addr().unwrap().to_string();
        let routes = Arc::new(Mutex::new(routes));
        let requests = Arc::new(Mutex::new(Vec::new()));
        let stop = Arc::new(AtomicBool::new(false));
        let (r, q, s, h) = (
            Arc::clone(&routes),
            Arc::clone(&requests),
            Arc::clone(&stop),
            host.clone(),
        );
        let thread = std::thread::spawn(move || {
            for stream in listener.incoming() {
                if s.load(Ordering::SeqCst) {
                    return;
                }
                if let Ok(stream) = stream {
                    handle(stream, &r, &q, &h);
                }
            }
        });
        Self {
            host,
            routes,
            requests,
            stop,
            thread: Some(thread),
        }
    }

    pub fn finish(mut self) -> Vec<Value> {
        self.stop.store(true, Ordering::SeqCst);
        let _ = TcpStream::connect(&self.host);
        if let Some(t) = self.thread.take() {
            t.join().unwrap();
        }
        self.requests.lock().unwrap().clone()
    }
}

fn handle(mut stream: TcpStream, routes: &Mutex<Vec<Route>>, seen: &Mutex<Vec<Value>>, host: &str) {
    let mut reader = BufReader::new(stream.try_clone().unwrap());
    let mut first = String::new();
    if reader.read_line(&mut first).unwrap_or(0) == 0 {
        return;
    }
    let mut parts = first.split_whitespace();
    let (method, target) = (
        parts.next().unwrap_or("").to_owned(),
        parts.next().unwrap_or("").to_owned(),
    );
    let (mut auth, mut length) = (String::new(), 0usize);
    loop {
        let mut line = String::new();
        reader.read_line(&mut line).unwrap();
        let line = line.trim_end();
        if line.is_empty() {
            break;
        }
        if let Some((k, v)) = line.split_once(':') {
            match k.to_ascii_lowercase().as_str() {
                "authorization" => auth = v.trim().to_owned(),
                "content-length" => length = v.trim().parse().unwrap_or(0),
                _ => {}
            }
        }
    }
    let mut body = vec![0u8; length];
    let _ = reader.read_exact(&mut body);
    let (path, query) = target.split_once('?').unwrap_or((target.as_str(), ""));
    seen.lock().unwrap().push(json!({
        "method": method, "path": path, "query": query, "auth": auth
    }));
    let page = query
        .split('&')
        .find_map(|kv| kv.strip_prefix("page="))
        .unwrap_or("");
    let route = routes
        .lock()
        .unwrap()
        .iter()
        .rev()
        .find(|r| r.path == path && r.page == page)
        .cloned();
    let Some(route) = route else {
        let _ = stream.write_all(
            b"HTTP/1.1 404 Not Found\r\nContent-Length: 0\r\nConnection: close\r\n\r\n",
        );
        return;
    };
    let payload = if route.body_b64.is_empty() {
        route
            .body
            .replace("$BASE", &format!("http://{host}"))
            .into_bytes()
    } else {
        base64::engine::general_purpose::STANDARD
            .decode(&route.body_b64)
            .unwrap()
    };
    let mut head = format!(
        "HTTP/1.1 {} {}\r\nContent-Length: {}\r\nConnection: close\r\n",
        route.status,
        reason(route.status),
        payload.len()
    );
    for (k, v) in &route.headers {
        head.push_str(&format!("{k}: {v}\r\n"));
    }
    head.push_str("\r\n");
    let _ = stream.write_all(head.as_bytes());
    let _ = stream.write_all(&payload);
    let _ = stream.flush();
}

