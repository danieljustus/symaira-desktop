use std::sync::{Arc, Mutex};
use std::time::Duration;

use axum::Router;
use axum::body::{Body, to_bytes};
use axum::extract::State;
use axum::http::{Request, StatusCode};
use axum::response::Response;
use axum::routing::post;
use serde::Deserialize;
use serde_json::Value;
use symdesk_protocol::{LocalEmbeddingError, embed_local_ollama, local_ollama_embeddings_endpoint};
use tokio::net::TcpListener;

#[derive(Deserialize)]
struct Fixture {
    schema_version: u32,
    cases: Vec<Case>,
}

#[derive(Deserialize)]
struct Case {
    id: String,
    model: String,
    inputs: Vec<String>,
    dimensions: usize,
    timeout_millis: u64,
    response_status: u16,
    response_body: String,
    #[serde(default)]
    response_delay_millis: u64,
    expected_vectors: Option<Vec<Option<Vec<f32>>>>,
    expected_error_code: Option<String>,
    expected_error_status: Option<u16>,
    expected_error_body: Option<String>,
    expected_failure_kind: Option<String>,
    expected_retryable: Option<bool>,
    request_method: Option<String>,
    request_path: Option<String>,
    request_content_type: Option<String>,
    request_accept: Option<String>,
    request_body: Option<Value>,
}

#[derive(Clone)]
struct MockState {
    status: StatusCode,
    body: String,
    delay: Duration,
    captured: Arc<Mutex<Option<CapturedRequest>>>,
}

struct CapturedRequest {
    method: String,
    path: String,
    content_type: Option<String>,
    accept: Option<String>,
    body: Value,
}

fn fixture() -> Fixture {
    let path = std::path::Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("../../testdata/port/retrieval/embedding-http.json");
    serde_json::from_str(&std::fs::read_to_string(path).expect("read Go embedding HTTP fixture"))
        .expect("decode Go embedding HTTP fixture")
}

async fn mock_embedding(State(state): State<MockState>, request: Request<Body>) -> Response<Body> {
    let (parts, body) = request.into_parts();
    let bytes = to_bytes(body, 1 << 20)
        .await
        .expect("read mock embedding request body");
    let body = serde_json::from_slice(&bytes).expect("decode mock embedding request JSON");
    *state.captured.lock().expect("capture request") = Some(CapturedRequest {
        method: parts.method.to_string(),
        path: parts.uri.path().to_owned(),
        content_type: parts
            .headers
            .get("content-type")
            .and_then(|value| value.to_str().ok())
            .map(str::to_owned),
        accept: parts
            .headers
            .get("accept")
            .and_then(|value| value.to_str().ok())
            .map(str::to_owned),
        body,
    });
    if !state.delay.is_zero() {
        tokio::time::sleep(state.delay).await;
    }
    let mut response = Response::new(Body::from(state.body));
    *response.status_mut() = state.status;
    response
}

async fn start_mock(
    case: &Case,
) -> (
    String,
    Arc<Mutex<Option<CapturedRequest>>>,
    tokio::task::JoinHandle<()>,
) {
    let listener = TcpListener::bind("127.0.0.1:0")
        .await
        .expect("bind loopback mock embedding server");
    let address = listener.local_addr().expect("mock server address");
    let captured = Arc::new(Mutex::new(None));
    let state = MockState {
        status: StatusCode::from_u16(case.response_status).expect("fixture HTTP status"),
        body: case.response_body.clone(),
        delay: Duration::from_millis(case.response_delay_millis),
        captured: captured.clone(),
    };
    let app = Router::new()
        .route("/v1/embeddings", post(mock_embedding))
        .with_state(state);
    let server = tokio::spawn(async move {
        axum::serve(listener, app)
            .await
            .expect("serve mock embeddings");
    });
    (
        format!("http://{address}/api/embeddings?ignored=1"),
        captured,
        server,
    )
}

fn failure_kind(error: &LocalEmbeddingError) -> &'static str {
    match error {
        LocalEmbeddingError::EmptyInputs => "empty_inputs",
        LocalEmbeddingError::InvalidEndpoint => "invalid_endpoint",
        LocalEmbeddingError::Transport(_) | LocalEmbeddingError::Timeout => "transport",
        LocalEmbeddingError::HttpStatus { .. } => "http_status",
        LocalEmbeddingError::EmbeddingCount { .. } => "embedding_count",
        LocalEmbeddingError::ResponseTooLarge | LocalEmbeddingError::InvalidResponse(_) => {
            "invalid_response"
        }
    }
}

#[tokio::test]
async fn replays_go_embedding_http_fixture_against_local_ollama_server() {
    let fixture = fixture();
    assert_eq!(fixture.schema_version, 1);
    assert_eq!(fixture.cases.len(), 16);
    for case in fixture.cases {
        let (url, captured, server) = start_mock(&case).await;
        let dimensions = (case.dimensions != 0).then_some(case.dimensions);
        let actual = embed_local_ollama(
            &url,
            &case.model,
            &case.inputs,
            dimensions,
            Duration::from_millis(case.timeout_millis),
        )
        .await;
        server.abort();

        match (&case.expected_vectors, &case.expected_failure_kind, actual) {
            (Some(expected), None, Ok(actual)) => {
                let expected = expected
                    .iter()
                    .map(|vector| vector.clone().unwrap_or_default())
                    .collect::<Vec<_>>();
                assert_eq!(actual, expected, "{} vectors", case.id);
            }
            (None, Some(expected_kind), Err(error)) => {
                assert_eq!(failure_kind(&error), expected_kind, "{} error", case.id);
                assert_eq!(
                    Some(error.contract_code()),
                    case.expected_error_code.as_deref(),
                    "{} error code",
                    case.id
                );
                assert_eq!(
                    Some(error.is_transient()),
                    case.expected_retryable,
                    "{} retryability",
                    case.id
                );
                if let Some(status) = case.expected_error_status {
                    assert!(
                        matches!(&error, LocalEmbeddingError::HttpStatus { status: got, .. } if *got == status),
                        "{} status error: {error}",
                        case.id
                    );
                }
                if let Some(expected_body) = case.expected_error_body.as_deref() {
                    assert!(
                        matches!(&error, LocalEmbeddingError::HttpStatus { body, .. } if body == expected_body),
                        "{} response body: {error}",
                        case.id
                    );
                }
                if case.expected_error_code.as_deref() == Some("transport_error") {
                    assert!(matches!(
                        &error,
                        LocalEmbeddingError::Timeout | LocalEmbeddingError::Transport(_)
                    ));
                }
            }
            (expected, failure, actual) => panic!(
                "{} fixture expects vectors={expected:?}, failure={failure:?}; got {actual:?}",
                case.id
            ),
        }

        if case.inputs.is_empty() {
            assert!(captured.lock().expect("read captured request").is_none());
        } else {
            let captured = captured
                .lock()
                .expect("read captured request")
                .take()
                .unwrap_or_else(|| panic!("{} request did not reach local server", case.id));
            assert_eq!(
                Some(captured.method.as_str()),
                case.request_method.as_deref(),
                "{} method",
                case.id
            );
            assert_eq!(
                Some(captured.path.as_str()),
                case.request_path.as_deref(),
                "{} path",
                case.id
            );
            assert_eq!(
                captured.content_type.as_deref(),
                case.request_content_type.as_deref(),
                "{} content type",
                case.id
            );
            assert_eq!(
                captured.accept.as_deref(),
                case.request_accept.as_deref(),
                "{} accept",
                case.id
            );
            assert_eq!(
                Some(&captured.body),
                case.request_body.as_ref(),
                "{} request body",
                case.id
            );
        }
    }
}

#[test]
fn embeddings_endpoint_keeps_existing_loopback_http_boundary() {
    for (input, expected) in [
        (
            "http://127.0.0.1:11434/api/embeddings?ignored=yes",
            Some("http://127.0.0.1:11434/v1/embeddings"),
        ),
        (
            "http://[::1]:11434/api/embeddings",
            Some("http://[::1]:11434/v1/embeddings"),
        ),
        (
            "http://localhost:11434",
            Some("http://127.0.0.1:11434/v1/embeddings"),
        ),
        ("https://127.0.0.1:11434", None),
        ("http://192.0.2.10:11434", None),
        ("http://user:secret@127.0.0.1:11434", None),
    ] {
        let actual = local_ollama_embeddings_endpoint(input).map(|uri| uri.to_string());
        assert_eq!(actual.as_deref(), expected, "endpoint {input}");
    }
}
