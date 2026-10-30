use serde::{Deserialize, Serialize};
use std::{
    collections::BTreeMap,
    fs,
    io::{Read, Write},
    net::TcpListener,
    path::{Path, PathBuf},
    thread,
    time::{Duration, Instant},
};
use symdesk_core::{ai::Provider, config::Config};
const CANARY: &str = "port-only-synthetic-provider-canary";
#[derive(Deserialize)]
struct Corpus {
    schema_version: u32,
    cases: Vec<Case>,
}
#[derive(Deserialize)]
struct Case {
    id: String,
    provider: String,
    url: String,
    env_url: String,
    model: String,
    env_model: String,
    max_tokens: i64,
    prompt: String,
    status: u16,
    response: String,
    chunks: Vec<String>,
    error: String,
    go_error_contains_credential: bool,
    one_shot: String,
    one_shot_error: String,
    requests: Vec<Request>,
}
#[derive(Debug, Deserialize, Serialize, PartialEq, Eq)]
struct Request {
    method: String,
    path: String,
    headers: BTreeMap<String, Vec<String>>,
    body: String,
}
fn corpus() -> Corpus {
    let path = std::env::var_os("PORT_FIXTURE_PATH")
        .map(PathBuf::from)
        .unwrap_or_else(|| {
            Path::new(env!("CARGO_MANIFEST_DIR")).join("../../testdata/port/ai/providers.json")
        });
    serde_json::from_slice(&fs::read(path).unwrap()).unwrap()
}
fn serve(listener: &TcpListener, status: u16, response: &str) -> Request {
    listener.set_nonblocking(true).unwrap();
    let deadline = Instant::now() + Duration::from_secs(5);
    let mut stream = loop {
        match listener.accept() {
            Ok((stream, _)) => break stream,
            Err(e) if e.kind() == std::io::ErrorKind::WouldBlock && Instant::now() < deadline => {
                thread::sleep(Duration::from_millis(2))
            }
            Err(e) => panic!("loopback request not received: {e}"),
        }
    };
    stream.set_nonblocking(false).unwrap();
    stream
        .set_read_timeout(Some(Duration::from_secs(3)))
        .unwrap();
    stream
        .set_write_timeout(Some(Duration::from_secs(3)))
        .unwrap();
    let mut input = Vec::new();
    let boundary = loop {
        let mut bytes = [0; 4096];
        let n = stream.read(&mut bytes).unwrap();
        assert!(n > 0, "early EOF");
        input.extend_from_slice(&bytes[..n]);
        if let Some(pos) = input.windows(4).position(|v| v == b"\r\n\r\n") {
            break pos + 4;
        }
        assert!(input.len() < 65536);
    };
    let head = String::from_utf8(input[..boundary].to_vec()).unwrap();
    let mut lines = head.split("\r\n");
    let first = lines.next().unwrap().split_whitespace().collect::<Vec<_>>();
    let mut headers = BTreeMap::<String, Vec<String>>::new();
    let mut length = 0;
    for line in lines.filter(|line| !line.is_empty()) {
        let (key, value) = line.split_once(':').unwrap();
        let key = key.to_lowercase();
        let value = value.trim();
        match key.as_str() {
            "content-length" => {
                length = value.parse().unwrap();
            }
            "host" | "user-agent" | "accept-encoding" => (),
            "x-api-key" | "authorization" => {
                assert!(
                    value.contains(CANARY),
                    "fixture must use only the injected credential"
                );
                headers
                    .entry(key)
                    .or_default()
                    .push("[REDACTED]".to_owned());
            }
            _ => {
                headers.entry(key).or_default().push(value.to_owned());
            }
        }
    }
    while input.len() < boundary + length {
        let mut bytes = [0; 4096];
        let n = stream.read(&mut bytes).unwrap();
        assert!(n > 0);
        input.extend_from_slice(&bytes[..n]);
    }
    let request = Request {
        method: first[0].to_owned(),
        path: first[1].to_owned(),
        headers,
        body: String::from_utf8(input[boundary..boundary + length].to_vec()).unwrap(),
    };
    write!(stream,"HTTP/1.1 {status} Test\r\nContent-Type: text/event-stream\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{response}",response.len()).unwrap();
    request
}
#[test]
fn replay_go_provider_requests_chunks_and_errors() {
    let corpus = corpus();
    assert_eq!(corpus.schema_version, 1);
    assert_eq!(corpus.cases.len(), 33);
    let mut leak_controls = 0;
    for case in corpus.cases {
        let listener = TcpListener::bind("127.0.0.1:0").unwrap();
        let endpoint = format!("http://{}", listener.local_addr().unwrap());
        let count = case.requests.len();
        let server = if count == 0 {
            None
        } else {
            Some(thread::spawn({
                let response = case.response.clone();
                move || {
                    (0..count)
                        .map(|_| serve(&listener, case.status, &response))
                        .collect::<Vec<_>>()
                }
            }))
        };
        let expand = |value: &str| value.replace("{server}", &endpoint);
        let mut config = Config::default();
        config.llm_provider = case.provider;
        config.ollama_url = expand(&case.url);
        config.llm_model = case.model;
        config.max_tokens = case.max_tokens;
        let environment = BTreeMap::from([
            ("SYMDESK_OLLAMA_URL".to_owned(), expand(&case.env_url)),
            ("SYMDESK_OLLAMA_MODEL".to_owned(), case.env_model),
            ("SYMDESK_ANTHROPIC_URL".to_owned(), endpoint),
        ]);
        let mut chunks = Vec::new();
        let result =
            Provider::resolve(&config, &environment, |_| CANARY.to_owned()).and_then(|provider| {
                provider.stream(&case.prompt, |chunk| {
                    chunks.push(chunk.to_owned());
                    Ok(())
                })
            });
        let one_shot = symdesk_core::ai::prompt_one(
            &config,
            &environment,
            |_| CANARY.to_owned(),
            &case.prompt,
        );
        let requests = server
            .into_iter()
            .flat_map(|server| server.join().unwrap())
            .collect::<Vec<_>>();
        let (one_shot, one_shot_error) = match one_shot {
            Ok(value) => (value, String::new()),
            Err(error) => (String::new(), error),
        };
        assert!(!one_shot_error.contains(CANARY));
        assert_eq!(one_shot, case.one_shot, "{}: one-shot result", case.id);
        assert_eq!(
            one_shot_error, case.one_shot_error,
            "{}: one-shot exact error",
            case.id
        );
        let error = result.err().unwrap_or_default();
        assert!(
            !error.contains(CANARY),
            "{}: credential must never appear in Rust error",
            case.id
        );
        if case.go_error_contains_credential {
            assert_eq!(case.id, "anthropic-unauthorized-redacted");
            leak_controls += 1;
        }
        assert_eq!(
            requests, case.requests,
            "{}: exact provider request transcript",
            case.id
        );
        assert_eq!(
            error, case.error,
            "{}: exact error after explicit SEC-002 redaction",
            case.id
        );
        assert_eq!(chunks, case.chunks, "{}: exact chunks", case.id);
    }
    assert_eq!(
        leak_controls, 1,
        "retain the measured Go credential-leak negative control"
    );
}
