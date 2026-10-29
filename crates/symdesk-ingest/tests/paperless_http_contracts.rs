use serde_json::{Value, json};
use std::{
    collections::BTreeMap,
    fs,
    io::{Read, Write},
    net::{TcpListener, TcpStream},
    path::Path,
    sync::{
        Arc, Mutex,
        atomic::{AtomicBool, Ordering},
    },
    thread,
    time::{Duration, Instant},
};
use symdesk_ingest::paperless::Client;
struct Server {
    base: String,
    requests: Arc<Mutex<Vec<Value>>>,
    stop: Arc<AtomicBool>,
    worker: Option<thread::JoinHandle<()>>,
}
impl Server {
    fn new(responses: Vec<Value>) -> Self {
        let listener = TcpListener::bind("127.0.0.1:0").unwrap();
        listener.set_nonblocking(true).unwrap();
        let base = format!("http://{}", listener.local_addr().unwrap());
        let requests = Arc::new(Mutex::new(Vec::new()));
        let record = requests.clone();
        let stop = Arc::new(AtomicBool::new(false));
        let shutdown = stop.clone();
        let worker = thread::spawn(move || {
            let deadline = Instant::now() + Duration::from_secs(10);
            for response in responses {
                let mut stream = loop {
                    if shutdown.load(Ordering::Relaxed) || Instant::now() > deadline {
                        return;
                    }
                    match listener.accept() {
                        Ok((s, _)) => break s,
                        Err(e) if e.kind() == std::io::ErrorKind::WouldBlock => {
                            thread::sleep(Duration::from_millis(1))
                        }
                        Err(e) => panic!("{e}"),
                    }
                };
                stream.set_nonblocking(false).unwrap();
                stream
                    .set_read_timeout(Some(Duration::from_secs(3)))
                    .unwrap();
                stream
                    .set_write_timeout(Some(Duration::from_secs(3)))
                    .unwrap();
                record.lock().unwrap().push(request(&mut stream));
                let code = response["status"].as_u64().unwrap();
                let reason = ureq::http::StatusCode::from_u16(code as u16)
                    .unwrap()
                    .canonical_reason()
                    .unwrap_or_default();
                let body = response["body"].as_str().unwrap();
                let mut headers =
                    BTreeMap::from([("Content-Type".to_owned(), "application/json".to_owned())]);
                if let Some(m) = response["headers"].as_object() {
                    for (k, v) in m {
                        headers.insert(k.clone(), v.as_str().unwrap().into());
                    }
                }
                write!(
                    stream,
                    "HTTP/1.1 {code} {reason}\r\nContent-Length: {}\r\nConnection: close\r\n",
                    body.len()
                )
                .unwrap();
                for (k, v) in headers {
                    write!(stream, "{k}: {v}\r\n").unwrap();
                }
                write!(stream, "\r\n{body}").unwrap();
            }
        });
        Self {
            base,
            requests,
            stop,
            worker: Some(worker),
        }
    }
}
impl Drop for Server {
    fn drop(&mut self) {
        self.stop.store(true, Ordering::Relaxed);
        self.worker.take().unwrap().join().unwrap();
    }
}
fn request(s: &mut TcpStream) -> Value {
    let mut raw = Vec::new();
    while !raw.ends_with(b"\r\n\r\n") {
        let mut b = [0];
        s.read_exact(&mut b).unwrap();
        raw.push(b[0]);
        assert!(raw.len() < 65536);
    }
    let raw = String::from_utf8(raw).unwrap();
    let mut lines = raw.split("\r\n");
    let mut first = lines.next().unwrap().split_whitespace();
    let method = first.next().unwrap();
    let target = first.next().unwrap();
    let (path, query) = target.split_once('?').unwrap_or((target, ""));
    let mut headers: BTreeMap<String, Vec<String>> = BTreeMap::new();
    let mut authorized = false;
    for line in lines.filter(|l| !l.is_empty()) {
        let (k, v) = line.split_once(':').unwrap();
        let k = k.to_ascii_lowercase();
        if k == "authorization" {
            assert_eq!(v.trim(), "Token synthetic-test-only");
            authorized = true;
            continue;
        }
        if k == "host" {
            continue;
        }
        headers.entry(k).or_default().push(v.trim().into());
    }
    assert!(authorized);
    json!({"method":method,"path":path,"query":query,"headers":headers,"body":""})
}
fn fixture() -> Value {
    serde_json::from_slice(
        &fs::read(
            Path::new(env!("CARGO_MANIFEST_DIR"))
                .join("../../testdata/port/ingest/paperless-http.json"),
        )
        .unwrap(),
    )
    .unwrap()
}
fn replay(f: &Value) -> Result<(), String> {
    for case in f["cases"].as_array().unwrap() {
        let input = &case["input"];
        let server = Server::new(input["responses"].as_array().unwrap().clone());
        let c = Client::new(&server.base, "synthetic-test-only");
        let id = input["id"].as_str().unwrap();
        let result = match input["op"].as_str().unwrap() {
            "documents" => c
                .list_documents(
                    input["since"].as_str().unwrap(),
                    input["limit"].as_u64().unwrap() as usize,
                    &input["filters"]
                        .as_array()
                        .map(|v| {
                            v.iter()
                                .map(|v| v.as_str().unwrap().into())
                                .collect::<Vec<String>>()
                        })
                        .unwrap_or_default(),
                )
                .map(|v| json!(v)),
            "get" => c.get_document(42).map(|v| json!(v)),
            "tags" => c.list_tags().map(|v| json!(v)),
            "correspondents" => c.list_correspondents().map(|v| json!(v)),
            "document_types" => c.list_document_types().map(|v| json!(v)),
            "storage_paths" => c.list_storage_paths().map(|v| json!(v)),
            "download" => {
                let mut body = Vec::new();
                c.download_document(42, &mut body)
                    .map(|meta| json!({"meta":meta,"body":String::from_utf8(body).unwrap()}))
            }
            op => panic!("unknown operation {op}"),
        };
        let (actual, error) = match result {
            Ok(v) => (v, String::new()),
            Err(e) => {
                let v = if input["op"] == "download" {
                    json!({"meta":{},"body":""})
                } else {
                    Value::Null
                };
                (v, e)
            }
        };
        if actual != case["result"] {
            return Err(format!(
                "{id} result\nactual {actual:#}\nexpected {:#}",
                case["result"]
            ));
        }
        if json!(error) != case["error"] {
            return Err(format!("{id} error {error:?} != {}", case["error"]));
        }
        let requests = server.requests.lock().unwrap().clone();
        if json!(requests) != case["requests"] {
            return Err(format!(
                "{id} requests\nactual {:#}\nexpected {:#}",
                json!(requests),
                case["requests"]
            ));
        }
    }
    Ok(())
}
#[test]
fn paperless_http_go_contract() {
    let f = fixture();
    assert_eq!(f["cases"].as_array().unwrap().len(), 16);
    replay(&f).unwrap();
}
#[test]
fn paperless_http_negative_control() {
    let mut f = fixture();
    replay(&f).unwrap();
    f["cases"][0]["requests"][0]["query"] = json!("mutated");
    assert!(
        replay(&f)
            .unwrap_err()
            .contains("documents-pagination requests")
    );
}
