//! Loopback fake Ollama `/api/generate` provider for the admin replay. It
//! mirrors `adminLLMAnswer` of the Go recorder (`admin_bodies_ai_port_test.go`):
//! the answer is derived from marker words in the prompt.
#![allow(dead_code)]

use std::{
    io::{BufRead, BufReader, Read, Write},
    net::{TcpListener, TcpStream},
    sync::{
        Arc,
        atomic::{AtomicBool, Ordering},
    },
    thread,
    time::Duration,
};

use serde_json::{Value, json};

pub struct LlmFake {
    pub url: String,
    stop: Arc<AtomicBool>,
    worker: Option<thread::JoinHandle<()>>,
}

impl LlmFake {
    pub fn start() -> Self {
        let listener = TcpListener::bind("127.0.0.1:0").expect("bind fake llm server");
        listener.set_nonblocking(true).expect("nonblocking listener");
        let url = format!("http://{}", listener.local_addr().expect("local address"));
        let stop = Arc::new(AtomicBool::new(false));
        let thread_stop = Arc::clone(&stop);
        let worker = thread::spawn(move || {
            while !thread_stop.load(Ordering::Relaxed) {
                match listener.accept() {
                    Ok((stream, _)) => {
                        thread::spawn(move || serve(stream));
                    }
                    Err(error) if error.kind() == std::io::ErrorKind::WouldBlock => {
                        thread::sleep(Duration::from_millis(5));
                    }
                    Err(error) => panic!("accept fake llm request: {error}"),
                }
            }
        });
        Self {
            url,
            stop,
            worker: Some(worker),
        }
    }
}

impl Drop for LlmFake {
    fn drop(&mut self) {
        self.stop.store(true, Ordering::Relaxed);
        if let Some(worker) = self.worker.take() {
            let _ = worker.join();
        }
    }
}

/// Go `adminLLMAnswer`: `None` is an HTTP 500.
fn answer(prompt: &str) -> Option<String> {
    if prompt.contains("LLM-FAIL") {
        return None;
    }
    if prompt.contains("Extract the value for the property") {
        if prompt.contains("LLM-EMPTY") {
            return Some(String::new());
        }
        if prompt.contains("LLM-PADDED") {
            return Some("  \n padded value \n".to_owned());
        }
        let title = prompt
            .lines()
            .find_map(|line| line.strip_prefix("Title: "))
            .unwrap_or("untitled");
        return Some(format!("filled:{title}"));
    }
    if prompt.contains("LLM-BLANK") {
        return Some("  \n".to_owned());
    }
    let paths: Vec<&str> = prompt
        .lines()
        .filter_map(|line| line.strip_prefix("--- Source [["))
        .filter_map(|rest| rest.split_once("]]").map(|(path, _)| path))
        .collect();
    let mut out = format!("# Generated\n\nsources={}\n\n## Sources\n", paths.len());
    for path in &paths {
        out.push_str(&format!("[[{path}]]\n"));
    }
    if prompt.contains("LLM-STRAY") {
        out.push_str("[[stray/outside.md]]\n");
    }
    Some(out)
}

fn serve(mut stream: TcpStream) {
    if stream.set_nonblocking(false).is_err()
        || stream
            .set_read_timeout(Some(Duration::from_secs(3)))
            .is_err()
    {
        return;
    }
    let Ok(clone) = stream.try_clone() else {
        return;
    };
    let mut reader = BufReader::new(clone);
    let mut request_line = String::new();
    if reader.read_line(&mut request_line).is_err() {
        return;
    }
    let mut length = 0usize;
    loop {
        let mut line = String::new();
        if reader.read_line(&mut line).is_err() || line == "\r\n" || line.is_empty() {
            break;
        }
        if let Some((name, value)) = line.split_once(':')
            && name.trim().eq_ignore_ascii_case("content-length")
        {
            length = value.trim().parse().unwrap_or(0);
        }
    }
    let mut body = vec![0; length];
    if reader.read_exact(&mut body).is_err() {
        return;
    }
    if !request_line.contains("/api/generate") {
        let _ = write!(
            stream,
            "HTTP/1.1 404 Not Found\r\nContent-Length: 0\r\nConnection: close\r\n\r\n"
        );
        return;
    }
    let body: Value = serde_json::from_slice(&body).unwrap_or(Value::Null);
    let prompt = body["prompt"].as_str().unwrap_or_default();
    let (status, payload) = match answer(prompt) {
        Some(text) => (
            "200 OK",
            format!("{}\n", json!({"model": "fake", "response": text, "done": true})),
        ),
        None => (
            "500 Internal Server Error",
            "{\"error\":\"fake failure\"}".to_owned(),
        ),
    };
    let _ = write!(
        stream,
        "HTTP/1.1 {status}\r\nContent-Type: application/x-ndjson\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{payload}",
        payload.len()
    );
}
