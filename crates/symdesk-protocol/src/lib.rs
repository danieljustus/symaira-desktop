#![deny(unsafe_code)]

//! Compatibility-focused HTTP adapter for the representative SymDesk API.
//!
//! The adapter deliberately owns the public wire contract instead of exposing
//! Axum defaults: authentication, headers, path confinement, snapshots, and
//! file ranges are all tested at the HTTP boundary.

#[cfg(any(test, unix, target_os = "windows"))]
mod mime;
#[cfg(target_os = "windows")]
mod native_mime;
mod snapshot_cache;
#[cfg(test)]
mod snapshot_cache_contracts;

use snapshot_cache::{RootIdentity, SnapshotCache, SnapshotPayload};

use std::{
    collections::BTreeMap,
    ffi::{OsStr, OsString},
    fmt::Write as _,
    fs,
    future::poll_fn,
    io::{self, Read, Seek, SeekFrom, Write},
    net::SocketAddr,
    path::{Component, Path, PathBuf},
    pin::Pin,
    process::Stdio,
    sync::{
        Arc, Mutex,
        atomic::{AtomicU64, Ordering},
    },
    task::{Context, Poll},
    time::{Duration, SystemTime, UNIX_EPOCH},
};

use axum::{
    Router,
    body::{Body, Bytes, to_bytes},
    extract::{DefaultBodyLimit, Extension, Multipart, Path as AxumPath, Query, State},
    http::{HeaderMap, HeaderValue, Method, Request, StatusCode, header},
    middleware::{self, Next},
    response::Response,
    routing::{get, post},
};
use flate2::{Compression, write::GzEncoder};
use futures_core::Stream;
use httpdate::{fmt_http_date, parse_http_date};
use hyper::server::conn::http1::Builder as ConnectionBuilder;
use hyper_util::{
    rt::{TokioIo, TokioTimer},
    service::TowerToHyperService,
};
use serde::{Deserialize, Serialize};
use serde_json::json;
use symdesk_index::{IndexedDocument, Sidecar};
use symdesk_vault::{Notebook, parse_bytes, parse_notebook, secure_path, walk_markdown_with};
use time::{OffsetDateTime, format_description::well_known::Rfc3339};
use tokio::{
    io::{AsyncBufRead, AsyncBufReadExt, AsyncRead, AsyncReadExt, AsyncWriteExt},
    process::Command as TokioCommand,
    sync::mpsc,
};
use tower::{Service as _, ServiceExt as _};

const MAX_NOTE_BYTES: u64 = 8 << 20;
const MAX_SNAPSHOT_BYTES: u64 = 16 << 20;
const MAX_NOTEBOOK_FILE_BYTES: u64 = 64 << 20;
const MAX_SHARE_STORE_BYTES: u64 = 16 << 20;
const MAX_WORKER_LEASE_BODY_BYTES: usize = 64 << 10;
const MAX_WORKER_FAIL_BODY_BYTES: usize = 256 << 10;
const MAX_WORKER_COMPLETE_BODY_BYTES: usize = 24 << 20;
const MAX_COMMAND_BODY_BYTES: usize = (2 << 20) + 1;
const MAX_AI_TRANSFORM_BODY_BYTES: usize = 256 << 10;
const MAX_COMMAND_OUTPUT_BYTES: usize = 32 << 20;
const MAX_COMMAND_STDERR_BYTES: usize = 1 << 20;
const MAX_UPLOAD_BYTES: u64 = 100 << 20;
const MAX_MULTIPART_REQUEST_BYTES: usize = (100 << 20) + (1 << 20);
const SNAPSHOT_NOTE_OVERHEAD_BYTES: u64 = 128;
const READ_TIMEOUT: Duration = Duration::from_secs(120);
const COMMAND_TIMEOUT: Duration = Duration::from_secs(5 * 60);
const HTTP_HEADER_READ_TIMEOUT: Duration = Duration::from_secs(10);
const HTTP_MAX_HEADER_BYTES: usize = 1 << 20;
const HTTP_MAX_HEADER_FIELDS: usize = 128;
const SNAPSHOT_TOO_LARGE: &str = "snapshot exceeds 16 MiB limit";
static PUT_TEMP_COUNTER: AtomicU64 = AtomicU64::new(0);

#[derive(Clone, Debug)]
pub struct HttpConfig {
    pub listen_address: String,
    pub vault_root: PathBuf,
    pub token: String,
    pub worker_token: Option<String>,
    pub version: String,
}

struct AppState {
    vault_root: PathBuf,
    token: Arc<[u8]>,
    worker_token: Option<Arc<[u8]>>,
    version: String,
    auth_failures: Mutex<AuthThrottle>,
    snapshot_cache: SnapshotCache,
    job_retry: Mutex<()>,
    share_write: Mutex<()>,
}

#[derive(Clone, Debug, PartialEq, Eq)]
enum AuthRole {
    Admin,
    Worker,
    User { name: String, roles: Vec<String> },
}

impl AuthRole {
    fn has_role(&self, expected: &str) -> bool {
        match self {
            Self::Admin => expected == "admin" || expected == "user",
            Self::Worker => expected == "worker",
            Self::User { roles, .. } => roles.iter().any(|role| role == expected),
        }
    }

    fn is_admin(&self) -> bool {
        self.has_role("admin")
    }

    fn is_worker(&self) -> bool {
        self.has_role("worker")
    }

    fn is_named_user(&self) -> bool {
        matches!(self, Self::User { .. })
    }

    fn name(&self) -> Option<&str> {
        match self {
            Self::Admin => Some("admin"),
            Self::Worker => Some("worker"),
            Self::User { name, .. } => Some(name),
        }
    }
}

#[derive(Debug, Deserialize)]
struct StoredUser {
    #[serde(default)]
    name: String,
    #[serde(default)]
    token_hash: String,
    #[serde(default)]
    roles: Vec<String>,
}

#[derive(Debug, Deserialize)]
struct DocumentRule {
    path: String,
    #[serde(default)]
    owner: String,
    #[serde(default)]
    read_users: Vec<String>,
    #[serde(default)]
    read_groups: Vec<String>,
    #[serde(default)]
    write_users: Vec<String>,
    #[serde(default)]
    write_groups: Vec<String>,
}

#[derive(Debug, Deserialize)]
struct PermissionGroup {
    name: String,
    #[serde(default)]
    members: Vec<String>,
}

#[derive(Debug, Default)]
struct AuthThrottle {
    entries: std::collections::HashMap<String, AuthFailure>,
}

#[derive(Debug)]
struct AuthFailure {
    auth: FailureBucket,
    share: FailureBucket,
    ai: FailureBucket,
    last_seen: SystemTime,
}

#[derive(Debug)]
struct FailureBucket {
    count: u32,
    window_start: SystemTime,
    blocked_until: SystemTime,
}

const AUTH_WINDOW: Duration = Duration::from_secs(10);
const AUTH_BLOCK: Duration = Duration::from_secs(30);
const AUTH_MAX: u32 = 5;
const SHARE_WINDOW: Duration = Duration::from_secs(30);
const SHARE_BLOCK: Duration = Duration::from_secs(60);
const SHARE_MAX: u32 = 3;
const AI_WINDOW: Duration = Duration::from_secs(30);
const AI_BLOCK: Duration = Duration::from_secs(60);
const AI_MAX: u32 = 12;
const AUTH_MAX_ENTRIES: usize = 5_000;

#[derive(Debug, Deserialize)]
struct FileQuery {
    path: Option<String>,
}

#[derive(Debug, Deserialize)]
struct JobQuery {
    limit: Option<String>,
    offset: Option<String>,
    id: Option<String>,
}

#[derive(Debug, Deserialize)]
struct JobRetryQuery {
    id: Option<String>,
}

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
struct WorkerFailRequest {
    #[serde(default)]
    job_id: String,
    #[serde(default)]
    worker_id: String,
    #[serde(default)]
    error: String,
    #[serde(default)]
    retry: bool,
}

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
struct WorkerLeaseRequest {
    worker_id: Option<String>,
    capabilities: Option<Vec<String>>,
}

#[derive(Debug, Deserialize)]
struct WorkerCompleteRequest {
    #[serde(default, deserialize_with = "deserialize_null_string")]
    job_id: String,
    #[serde(default, deserialize_with = "deserialize_null_string")]
    worker_id: String,
    #[serde(default, deserialize_with = "deserialize_null_string")]
    text: String,
    #[serde(default, deserialize_with = "deserialize_null_string")]
    engine: String,
    #[serde(default, deserialize_with = "deserialize_null_string")]
    model: String,
}

#[derive(Debug, Serialize, Deserialize)]
struct JobRecord {
    #[serde(default)]
    id: String,
    #[serde(default)]
    schema_version: i64,
    #[serde(default)]
    status: String,
    #[serde(default)]
    source_path: String,
    #[serde(default)]
    original_name: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    content_type: String,
    #[serde(default)]
    capability: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    worker_id: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    engine: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    model: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    note_path: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    error: String,
    #[serde(default = "go_zero_time")]
    created_at: String,
    #[serde(default = "go_zero_time")]
    updated_at: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    lease_until: Option<String>,
}

#[derive(Serialize)]
struct JobPage {
    jobs: Vec<JobRecord>,
    total: usize,
    limit: usize,
    offset: usize,
}

#[derive(Debug, Serialize, Deserialize)]
struct Snapshot {
    generated_at: String,
    notes: Vec<SnapshotNote>,
}

#[derive(Debug, Serialize, Deserialize)]
struct SnapshotNote {
    path: String,
    content: String,
    modified_at: String,
}

#[derive(Debug, Serialize)]
struct WorkerSnapshot {
    notes: Vec<SnapshotNote>,
    generated_at: String,
}

#[derive(Debug)]
enum PathError {
    Invalid,
}

#[derive(Debug, PartialEq, Eq)]
enum RangeError {
    Invalid,
    NoOverlap,
}

/// Starts the representative HTTP server and waits for SIGINT/SIGTERM.
///
/// The listener is bound before readiness is printed, so port `0` is safe for
/// the differential harness and the emitted address is the actual socket.
pub async fn run(config: HttpConfig) -> Result<(), String> {
    let root = fs::canonicalize(&config.vault_root)
        .map_err(|error| format!("resolve vault root: {error}"))?;
    if !root.is_dir() {
        return Err("vault root is not a directory".to_owned());
    }
    validate_tokens(&config.token, config.worker_token.as_deref())?;
    refresh_server_index(&root)?;
    let address: SocketAddr = config
        .listen_address
        .parse()
        .map_err(|error| format!("invalid listen address: {error}"))?;
    let listener = tokio::net::TcpListener::bind(address)
        .await
        .map_err(|error| format!("bind HTTP listener: {error}"))?;
    let actual = listener
        .local_addr()
        .map_err(|error| format!("read HTTP listener address: {error}"))?;
    let state = Arc::new(AppState {
        vault_root: root.clone(),
        token: Arc::from(config.token.into_bytes()),
        worker_token: config
            .worker_token
            .map(|token| Arc::from(token.into_bytes())),
        version: config.version,
        auth_failures: Mutex::new(AuthThrottle::default()),
        snapshot_cache: SnapshotCache::new(&root),
        job_retry: Mutex::new(()),
        share_write: Mutex::new(()),
    });
    let app = router(state);
    eprintln!("LISTENING http://{actual}");

    let mut make_service = app.into_make_service_with_connect_info::<SocketAddr>();
    let mut connection_builder = ConnectionBuilder::new();
    connection_builder
        .timer(TokioTimer::new())
        .header_read_timeout(Some(HTTP_HEADER_READ_TIMEOUT))
        .max_buf_size(HTTP_MAX_HEADER_BYTES)
        .max_headers(HTTP_MAX_HEADER_FIELDS);
    let connection_builder = connection_builder;
    let (shutdown_tx, shutdown_rx) = tokio::sync::watch::channel(());
    let shutdown = shutdown_signal();
    tokio::pin!(shutdown);
    let mut active = tokio::task::JoinSet::new();

    loop {
        while active.try_join_next().is_some() {}
        let accepted = tokio::select! {
            result = listener.accept() => Some(result),
            _ = &mut shutdown => None,
        };
        let Some(accepted) = accepted else {
            drop(shutdown_tx);
            while active.join_next().await.is_some() {}
            break;
        };
        let (io, remote_addr) = match accepted {
            Ok(connection) => connection,
            Err(error) => {
                eprintln!("accept HTTP connection failed: {error}");
                tokio::time::sleep(Duration::from_secs(1)).await;
                continue;
            }
        };

        let io = TokioIo::new(io);
        let tower_service = make_service
            .call(remote_addr)
            .await
            .unwrap_or_else(|error| match error {})
            .map_request(|request: hyper::Request<hyper::body::Incoming>| request.map(Body::new));
        let hyper_service = TowerToHyperService::new(tower_service);
        let builder = connection_builder.clone();
        let mut connection_shutdown = shutdown_rx.clone();
        active.spawn(async move {
            let connection = builder.serve_connection(io, hyper_service);
            let mut connection = std::pin::pin!(connection);
            tokio::select! {
                result = &mut connection => {
                    if let Err(error) = result {
                        eprintln!("HTTP connection failed: {error}");
                    }
                }
                _ = connection_shutdown.changed() => {
                    connection.as_mut().graceful_shutdown();
                    let _ = connection.await;
                }
            }
        });
    }
    Ok(())
}

fn refresh_server_index(vault_root: &Path) -> Result<(), String> {
    let directory = vault_root.join(".symdesk/server");
    let path = directory.join("sidecar.db");
    let mut sidecar = symdesk_index::Sidecar::open(&path)
        .map_err(|error| format!("open server sidecar: {error}"))?;
    sidecar
        .refresh_index(vault_root)
        .map_err(|error| format!("index vault: {error}"))
}

fn router(state: Arc<AppState>) -> Router {
    let protected = Router::new()
        .route("/api/v1/status", get(handle_status))
        .route("/api/v1/snapshot", get(handle_snapshot))
        .route("/api/v1/files", get(handle_file).put(handle_put_file))
        .route("/api/v1/jobs", get(handle_jobs))
        .route("/api/v1/jobs/retry", axum::routing::post(handle_retry_job))
        .route("/api/v1/command", post(handle_command_validation))
        .route("/api/v1/ai/transform", post(handle_ai_transform))
        .route("/api/v1/worker/input", get(handle_worker_input))
        .route(
            "/api/v1/worker/fail",
            axum::routing::post(handle_worker_fail),
        )
        .route(
            "/api/v1/worker/lease",
            axum::routing::post(handle_worker_lease),
        )
        .route(
            "/api/v1/worker/complete",
            axum::routing::post(handle_worker_complete),
        )
        .route(
            "/api/v1/ingest",
            post(handle_ingest).layer(DefaultBodyLimit::max(MAX_MULTIPART_REQUEST_BYTES)),
        )
        .route("/api/v1/notebooks", get(handle_notebooks))
        .route("/api/v1/notebooks/{id}", get(handle_notebook))
        .route("/api/v1/shares", get(handle_shares))
        .route("/api/v1/share", post(handle_create_share))
        .route(
            "/api/v1/share/{id}",
            axum::routing::delete(handle_revoke_share),
        )
        .layer(middleware::from_fn_with_state(
            Arc::clone(&state),
            authenticate,
        ));
    Router::new()
        .route("/healthz", get(handle_health))
        .route("/s/{token}", get(handle_access_share))
        .merge(protected)
        .fallback(handle_not_found)
        .layer(middleware::from_fn(normalize_method_not_allowed))
        .layer(middleware::from_fn(request_timeout))
        .layer(middleware::from_fn(security_headers))
        .with_state(state)
}

async fn authenticate(
    State(state): State<Arc<AppState>>,
    mut request: Request<Body>,
    next: Next,
) -> Response {
    let provided = request
        .headers()
        .get(header::AUTHORIZATION)
        .and_then(|value| value.to_str().ok())
        .map(|value| value.trim().strip_prefix("Bearer ").unwrap_or(value).trim())
        .unwrap_or_default();
    let role = authenticate_named_user(&state, provided).or_else(|| {
        if constant_time_equal(provided.as_bytes(), &state.token) {
            Some(AuthRole::Admin)
        } else if state
            .worker_token
            .as_deref()
            .is_some_and(|token| constant_time_equal(provided.as_bytes(), token))
        {
            Some(AuthRole::Worker)
        } else {
            None
        }
    });
    let Some(role) = role else {
        let ip = client_ip(&request);
        let retry_after = state
            .auth_failures
            .lock()
            .ok()
            .and_then(|mut throttle| throttle.record(&ip));
        let status = if let Some(retry_after) = retry_after {
            let mut response = json_error(
                StatusCode::TOO_MANY_REQUESTS,
                "too many authentication attempts",
            );
            response.headers_mut().insert(
                header::RETRY_AFTER,
                HeaderValue::from_str(&retry_after_seconds(retry_after).to_string())
                    .unwrap_or_else(|_| HeaderValue::from_static("1")),
            );
            response
        } else {
            json_error(StatusCode::UNAUTHORIZED, "authentication required")
        };
        let mut response = status;
        response
            .headers_mut()
            .insert(header::WWW_AUTHENTICATE, HeaderValue::from_static("Bearer"));
        return response;
    };
    if !role.is_admin() && route_requires_admin(request.method(), request.uri().path()) {
        return json_error(StatusCode::FORBIDDEN, "admin role required");
    }
    if role.is_named_user()
        && !role.is_admin()
        && !named_user_route_allowed(&role, request.method(), request.uri().path())
    {
        return json_error(StatusCode::FORBIDDEN, "admin role required");
    }
    request.extensions_mut().insert(role);
    next.run(request).await
}

fn authenticate_named_user(state: &AppState, provided: &str) -> Option<AuthRole> {
    if provided.is_empty() {
        return None;
    }
    let root = open_current_root(state).ok()?;
    let users = read_acl_file::<StoredUser>(&root, ".symdesk/users.json").ok()?;
    let target = symdesk_vault::sha256_hex(provided.as_bytes());
    let mut matched = None;
    for user in users {
        let equal = constant_time_equal(user.token_hash.as_bytes(), target.as_bytes());
        if equal && matched.is_none() {
            matched = Some(AuthRole::User {
                name: user.name,
                roles: user.roles,
            });
        }
    }
    matched
}

fn named_user_route_allowed(role: &AuthRole, method: &Method, path: &str) -> bool {
    let read_method = method == Method::GET || method == Method::HEAD;
    if path == "/api/v1/status" && (method == Method::GET || method == Method::HEAD) {
        return true;
    }
    if path == "/api/v1/files"
        && (method == Method::GET || method == Method::HEAD || method == Method::PUT)
    {
        return true;
    }
    if path == "/api/v1/ai/transform" && method == Method::POST {
        return true;
    }
    if read_method
        && (path == "/api/v1/snapshot"
            || path == "/api/v1/notebooks"
            || path
                .strip_prefix("/api/v1/notebooks/")
                .is_some_and(|id| !id.is_empty() && !id.contains('/')))
    {
        return true;
    }
    if (path == "/api/v1/shares" && read_method)
        || (path == "/api/v1/share" && method == Method::POST)
        || (method == Method::DELETE
            && path
                .strip_prefix("/api/v1/share/")
                .is_some_and(|id| !id.is_empty() && !id.contains('/')))
    {
        return true;
    }
    role.is_worker()
        && ((path == "/api/v1/worker/lease" && method == Method::POST)
            || (path == "/api/v1/worker/input" && method == Method::GET)
            || (path == "/api/v1/worker/complete" && method == Method::POST)
            || (path == "/api/v1/worker/fail" && method == Method::POST))
}

fn route_requires_admin(method: &Method, path: &str) -> bool {
    (path == "/api/v1/jobs" && (method == Method::GET || method == Method::HEAD))
        || (path == "/api/v1/jobs/retry" && method == Method::POST)
        || (path == "/api/v1/ingest" && method == Method::POST)
        || (path == "/api/v1/command" && method == Method::POST)
}

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
struct RemoteCommandRequest {
    arguments: Option<Vec<String>>,
    stdin: Option<String>,
}

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
struct AiTransformRequest {
    #[serde(default, deserialize_with = "deserialize_null_string")]
    text: String,
    #[serde(default, deserialize_with = "deserialize_null_string")]
    intent: String,
}

async fn handle_ai_transform(
    State(state): State<Arc<AppState>>,
    request: Request<Body>,
) -> Response {
    if let Some(response) = ai_rate_limit_response(&state, &request) {
        return response;
    }
    let body = match read_capped_body(
        request.into_body(),
        MAX_AI_TRANSFORM_BODY_BYTES.saturating_add(1),
    )
    .await
    {
        Ok(body) => body,
        Err(error) => {
            return json_error(StatusCode::BAD_REQUEST, &format!("invalid JSON: {error}"));
        }
    };
    let mut input: AiTransformRequest = match decode_first_json_value(&body) {
        Ok(input) => input,
        Err(error) => {
            return json_error(StatusCode::BAD_REQUEST, &format!("invalid JSON: {error}"));
        }
    };
    input.text = input.text.trim().to_owned();
    input.intent = input.intent.trim().to_owned();
    if input.text.is_empty() {
        return json_error(StatusCode::BAD_REQUEST, "text is required");
    }
    if input.intent.is_empty() {
        input.intent = "summarize".to_owned();
    }

    // Go's service constructor falls back to DefaultConfig when config.Load
    // fails. Resolve once here, then render the fallback directly so a child
    // process cannot reload a changed provider config after this safety check.
    let config = ai_transform_config_or_default(load_ai_transform_config());
    let Some(text) = ai_transform_fallback_text(&config) else {
        return json_error(
            StatusCode::NOT_IMPLEMENTED,
            "configured AI provider streaming is not implemented",
        );
    };
    let mut answer = match serde_json::to_vec(&json!({"type": "answer", "text": text})) {
        Ok(body) => body,
        Err(error) => {
            return json_error(StatusCode::INTERNAL_SERVER_ERROR, &error.to_string());
        }
    };
    answer.push(b'\n');
    let (sender, receiver) = mpsc::channel(2);
    if sender.send(Ok(Bytes::from(answer))).await.is_err()
        || sender
            .send(Ok(Bytes::from_static(b"{\"type\":\"done\"}\n")))
            .await
            .is_err()
    {
        return json_error(
            StatusCode::INTERNAL_SERVER_ERROR,
            "failed to build transform stream",
        );
    }
    drop(sender);
    let mut response = Response::new(Body::from_stream(CommandBodyStream(receiver)));
    *response.status_mut() = StatusCode::OK;
    response.headers_mut().insert(
        header::CONTENT_TYPE,
        HeaderValue::from_static("application/x-ndjson"),
    );
    response
}

fn load_ai_transform_config() -> Result<symdesk_core::config::Config, String> {
    let environment = std::env::vars().collect::<BTreeMap<_, _>>();
    let path = PathBuf::from(symdesk_core::config::global_path(&environment));
    let input = match fs::read_to_string(path) {
        Ok(input) => Some(input),
        Err(error) if error.kind() == io::ErrorKind::NotFound => None,
        Err(error) => return Err(format!("failed to read config file: {error}")),
    };
    symdesk_core::config::load(input.as_deref(), &environment)
}

fn ai_transform_config_or_default(
    config: Result<symdesk_core::config::Config, String>,
) -> symdesk_core::config::Config {
    config.unwrap_or_default()
}

fn ai_transform_fallback_text(config: &symdesk_core::config::Config) -> Option<&'static str> {
    match config.llm_provider.as_str() {
        "" | "ollama" if config.ollama_url.is_empty() => Some(
            "⚠️ **AI feature not configured.**\n\nSet your Ollama endpoint in Settings → AI.\n",
        ),
        "anthropic" if !config.has_api_key() => Some(
            "⚠️ **AI feature not configured.**\n\nAnthropic API key could not be resolved (missing secret via symvault or environment variable).\n",
        ),
        _ => None,
    }
}

async fn handle_command_validation(
    State(state): State<Arc<AppState>>,
    request: Request<Body>,
) -> Response {
    let request =
        match tokio::time::timeout(READ_TIMEOUT, read_command_request(request.into_body())).await {
            Ok(Ok(request)) => request,
            Ok(Err(CommandBodyError::TooLarge)) => {
                return json_error(
                    StatusCode::BAD_REQUEST,
                    "invalid JSON: request body too large",
                );
            }
            Ok(Err(CommandBodyError::Malformed | CommandBodyError::Read)) => {
                return json_error(StatusCode::BAD_REQUEST, "invalid JSON: malformed request");
            }
            Err(_) => return json_error(StatusCode::REQUEST_TIMEOUT, "request timed out"),
        };
    let arguments = request.arguments.unwrap_or_default();
    let stdin = request.stdin.unwrap_or_default();
    if let Some(message) = validate_remote_command(&arguments) {
        return json_response(StatusCode::FORBIDDEN, json!({"error": message}));
    }
    if matches!(
        arguments.first().map(String::as_str),
        Some("ask" | "transform")
    ) {
        return stream_remote_command(&state, &arguments, &stdin).await;
    }
    execute_remote_command(&state, &arguments, &stdin).await
}

struct CommandBodyStream(mpsc::Receiver<Result<Bytes, io::Error>>);

impl Stream for CommandBodyStream {
    type Item = Result<Bytes, io::Error>;

    fn poll_next(mut self: Pin<&mut Self>, context: &mut Context<'_>) -> Poll<Option<Self::Item>> {
        self.0.poll_recv(context)
    }
}

async fn stream_remote_command(state: &AppState, arguments: &[String], stdin: &str) -> Response {
    let executable = match std::env::current_exe() {
        Ok(executable) => executable,
        Err(error) => return json_error(StatusCode::INTERNAL_SERVER_ERROR, &error.to_string()),
    };
    let mut args = arguments.to_vec();
    if !args.iter().any(|argument| argument == "--json") {
        args.push("--json".to_owned());
    }
    args.push("--vault".to_owned());
    args.push(state.vault_root.to_string_lossy().into_owned());
    let mut command = TokioCommand::new(executable);
    command
        .args(args)
        .env_clear()
        .envs(filtered_subprocess_env(
            std::env::vars_os(),
            &state.vault_root,
        ))
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .kill_on_drop(true);
    let mut child = match command.spawn() {
        Ok(child) => child,
        Err(error) => return json_error(StatusCode::INTERNAL_SERVER_ERROR, &error.to_string()),
    };
    let (Some(stdout), Some(stderr), stdin_pipe) =
        (child.stdout.take(), child.stderr.take(), child.stdin.take())
    else {
        let _ = child.kill().await;
        return json_error(
            StatusCode::INTERNAL_SERVER_ERROR,
            "failed to capture command streams",
        );
    };

    let (sender, receiver) = mpsc::channel(1);
    let stdin = stdin.as_bytes().to_vec();
    tokio::spawn(async move {
        let stderr_task = tokio::spawn(read_command_pipe(stderr, MAX_COMMAND_STDERR_BYTES));
        let stdin_task = tokio::spawn(async move { write_command_stdin(stdin_pipe, &stdin).await });
        let relay = relay_command_ndjson(stdout, &sender, &mut child, MAX_COMMAND_OUTPUT_BYTES);
        let relay_result = tokio::time::timeout(COMMAND_TIMEOUT, relay).await;
        match relay_result {
            Ok(Ok(Some(status))) => {
                let stderr = stderr_task
                    .await
                    .ok()
                    .and_then(Result::ok)
                    .map(|(bytes, _)| bytes)
                    .unwrap_or_default();
                let stdin_result = stdin_task.await.ok().and_then(Result::ok);
                let message = if !status.success() {
                    let text = String::from_utf8_lossy(&stderr).trim().to_owned();
                    if text.is_empty() {
                        status
                            .code()
                            .map(|code| format!("exit status {code}"))
                            .unwrap_or_else(|| "signal: killed".to_owned())
                    } else {
                        text
                    }
                } else if stdin_result.is_none() {
                    "failed to write command input".to_owned()
                } else {
                    String::new()
                };
                if !message.is_empty() {
                    let _ = send_command_error(&sender, &message).await;
                }
            }
            Ok(Ok(None)) => {}
            Ok(Err(error)) => {
                let _ = child.kill().await;
                let _ = child.wait().await;
                let _ = send_command_error(&sender, &error.to_string()).await;
            }
            Err(_) => {
                let _ = child.kill().await;
                let _ = child.wait().await;
                let _ = send_command_error(&sender, "signal: killed").await;
            }
        }
    });
    let mut response = Response::new(Body::from_stream(CommandBodyStream(receiver)));
    *response.status_mut() = StatusCode::OK;
    response.headers_mut().insert(
        header::CONTENT_TYPE,
        HeaderValue::from_static("application/x-ndjson"),
    );
    response
}

async fn relay_command_ndjson<R: AsyncRead + Unpin>(
    stdout: R,
    sender: &mpsc::Sender<Result<Bytes, io::Error>>,
    child: &mut tokio::process::Child,
    output_limit: usize,
) -> io::Result<Option<std::process::ExitStatus>> {
    let mut reader = tokio::io::BufReader::new(stdout);
    let mut written = 0_usize;
    let mut line = Vec::new();
    loop {
        line.clear();
        let read = tokio::select! {
            biased;
            _ = sender.closed() => {
                let _ = child.kill().await;
                let _ = child.wait().await;
                return Ok(None);
            }
            read = read_limited_line(&mut reader, &mut line, output_limit - written + 1) => read?,
        };
        if read == 0 {
            break;
        }
        if line.len() > output_limit - written {
            let _ = child.kill().await;
            // Go drains stdout after killing to allow the process to finish cleanly.
            let mut discard = [0_u8; 16 << 10];
            while reader.read(&mut discard).await? != 0 {}
            let _ = child.wait().await;
            send_command_error(sender, "command output exceeded 32 MiB").await?;
            return Ok(None);
        }
        written += line.len();
        if sender
            .send(Ok(Bytes::copy_from_slice(&line)))
            .await
            .is_err()
        {
            let _ = child.kill().await;
            let _ = child.wait().await;
            return Ok(None);
        }
    }
    child.wait().await.map(Some)
}

async fn read_limited_line<R: AsyncBufRead + Unpin>(
    reader: &mut R,
    line: &mut Vec<u8>,
    limit: usize,
) -> io::Result<usize> {
    loop {
        let available = reader.fill_buf().await?;
        if available.is_empty() {
            return Ok(line.len());
        }
        let newline = available.iter().position(|byte| *byte == b'\n');
        let count = newline.map_or(available.len(), |index| index + 1);
        let keep = count.min(limit.saturating_sub(line.len()));
        line.extend_from_slice(&available[..keep]);
        reader.consume(count);
        if keep < count || newline.is_some() {
            return Ok(line.len());
        }
    }
}

async fn send_command_error(
    sender: &mpsc::Sender<Result<Bytes, io::Error>>,
    message: &str,
) -> Result<(), io::Error> {
    let mut event = serde_json::to_vec(&json!({"type": "error", "message": message}))
        .map_err(io::Error::other)?;
    event.push(b'\n');
    sender
        .send(Ok(Bytes::from(event)))
        .await
        .map_err(|_| io::Error::new(io::ErrorKind::BrokenPipe, "client disconnected"))
}

enum CommandBodyError {
    TooLarge,
    Malformed,
    Read,
}

async fn read_command_request(body: Body) -> Result<RemoteCommandRequest, CommandBodyError> {
    let mut chunks = body.into_data_stream();
    let mut bytes = Vec::with_capacity(4096);
    loop {
        let next = poll_fn(|context| Pin::new(&mut chunks).poll_next(context)).await;
        let Some(next) = next else {
            return parse_command_request(&bytes).map_err(|_| CommandBodyError::Malformed);
        };
        let chunk = next.map_err(|_| CommandBodyError::Read)?;
        let available = MAX_COMMAND_BODY_BYTES.saturating_sub(bytes.len());
        bytes.extend_from_slice(&chunk[..chunk.len().min(available)]);
        match parse_command_request(&bytes) {
            Ok(request) => return Ok(request),
            Err(error) if error.is_eof() && bytes.len() < MAX_COMMAND_BODY_BYTES => {}
            Err(error) if error.is_eof() => return Err(CommandBodyError::TooLarge),
            Err(_) => return Err(CommandBodyError::Malformed),
        }
        if chunk.len() > available {
            return Err(CommandBodyError::TooLarge);
        }
    }
}

fn parse_command_request(body: &[u8]) -> Result<RemoteCommandRequest, serde_json::Error> {
    let mut decoder = serde_json::Deserializer::from_slice(body);
    RemoteCommandRequest::deserialize(&mut decoder)
}

fn validate_remote_command(args: &[String]) -> Option<String> {
    if args.is_empty() {
        return Some("command is required".to_owned());
    }
    if args.iter().any(|arg| {
        let lower = arg.to_ascii_lowercase();
        lower == "--vault"
            || lower.starts_with("--vault=")
            || lower == "--output"
            || lower.starts_with("--output=")
    }) {
        return Some("server-controlled path flags are not allowed".to_owned());
    }
    let subcommand = args
        .get(1)
        .filter(|argument| !argument.starts_with('-'))
        .map(String::as_str)
        .unwrap_or("");
    let available = match args[0].as_str() {
        "doctor" | "ls" | "search" | "backlinks" | "graph" | "similar" | "duplicates"
        | "restore" => subcommand.is_empty(),
        "transform" | "ask" => true,
        "note" => matches!(subcommand, "new" | "move" | "delete" | "daily"),
        "paperless" => subcommand == "import",
        "props" => matches!(subcommand, "get" | "edit"),
        "relations" => subcommand == "inverse",
        "views" => matches!(
            subcommand,
            "list" | "get" | "save" | "delete" | "new-entry" | "siblings" | "exec"
        ),
        "docs" => matches!(subcommand, "list" | "review"),
        "doc" => matches!(
            subcommand,
            "status" | "due" | "type" | "correspondent" | "tag" | "asn"
        ),
        "tags" => matches!(subcommand, "rename" | "merge" | "delete"),
        "conflict" => subcommand == "resolve",
        "history" => matches!(subcommand, "" | "prune" | "show"),
        "trash" => matches!(subcommand, "list" | "restore" | "delete"),
        _ => return Some(format!("command {:?} is not available remotely", args[0])),
    };
    if !available {
        return Some(format!(
            "subcommand {:?} is not available remotely",
            subcommand
        ));
    }
    None
}

fn filtered_subprocess_env<I>(parent: I, vault_root: &Path) -> Vec<(OsString, OsString)>
where
    I: IntoIterator<Item = (OsString, OsString)>,
{
    let mut environment: Vec<_> = parent
        .into_iter()
        .filter(|(key, _)| {
            key != OsStr::new("SYMDESK_SERVER_TOKEN") && key != OsStr::new("SYMDESK_WORKER_TOKEN")
        })
        .collect();
    environment.push((
        OsString::from("SYMDESK_SIDECAR"),
        vault_root
            .join(".symdesk/server/sidecar.db")
            .into_os_string(),
    ));
    environment
}

async fn execute_remote_command(state: &AppState, arguments: &[String], stdin: &str) -> Response {
    let executable = match std::env::current_exe() {
        Ok(executable) => executable,
        Err(error) => {
            return json_error(StatusCode::UNPROCESSABLE_ENTITY, &error.to_string());
        }
    };
    let mut args = arguments.to_vec();
    if !args.iter().any(|argument| argument == "--json") {
        args.push("--json".to_owned());
    }
    args.push("--vault".to_owned());
    args.push(state.vault_root.to_string_lossy().into_owned());

    let mut command = TokioCommand::new(executable);
    command
        .args(args)
        .env_clear()
        .envs(filtered_subprocess_env(
            std::env::vars_os(),
            &state.vault_root,
        ))
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .kill_on_drop(true);
    let mut child = match command.spawn() {
        Ok(child) => child,
        Err(error) => {
            return json_error(StatusCode::UNPROCESSABLE_ENTITY, &error.to_string());
        }
    };
    let Some(stdout) = child.stdout.take() else {
        let _ = child.kill().await;
        return json_error(
            StatusCode::INTERNAL_SERVER_ERROR,
            "failed to capture command output",
        );
    };
    let Some(stderr) = child.stderr.take() else {
        let _ = child.kill().await;
        return json_error(
            StatusCode::INTERNAL_SERVER_ERROR,
            "failed to capture command errors",
        );
    };
    let stdin_pipe = child.stdin.take();

    let process = async {
        let (stdout, stderr, stdin_result, status) = tokio::join!(
            read_command_pipe(stdout, MAX_COMMAND_OUTPUT_BYTES),
            read_command_pipe(stderr, MAX_COMMAND_STDERR_BYTES),
            write_command_stdin(stdin_pipe, stdin.as_bytes()),
            child.wait(),
        );
        let (stdout, stdout_overflow) = stdout?;
        let (stderr, _) = stderr?;
        stdin_result?;
        let status = status?;
        Ok::<_, io::Error>((stdout, stdout_overflow, stderr, status))
    };
    let captured = match tokio::time::timeout(COMMAND_TIMEOUT, process).await {
        Ok(Ok(captured)) => captured,
        Ok(Err(error)) => {
            let _ = child.kill().await;
            let _ = child.wait().await;
            return json_error(StatusCode::UNPROCESSABLE_ENTITY, &error.to_string());
        }
        Err(_) => {
            let _ = child.kill().await;
            let _ = child.wait().await;
            return json_error(StatusCode::UNPROCESSABLE_ENTITY, "signal: killed");
        }
    };
    let (stdout, stdout_overflow, stderr, status) = captured;
    if stdout_overflow {
        return json_error(
            StatusCode::PAYLOAD_TOO_LARGE,
            "command output exceeded 32 MiB",
        );
    }
    if !status.success() {
        let message = String::from_utf8_lossy(&stderr).trim().to_owned();
        if message.is_empty() {
            let message = status
                .code()
                .map(|code| format!("exit status {code}"))
                .unwrap_or_else(|| "signal: killed".to_owned());
            return json_error(StatusCode::UNPROCESSABLE_ENTITY, &message);
        }
        return json_error(StatusCode::UNPROCESSABLE_ENTITY, &message);
    }
    bytes_response(
        StatusCode::OK,
        vec![
            (header::CONTENT_TYPE, "application/json".to_owned()),
            (header::CONTENT_LENGTH, stdout.len().to_string()),
        ],
        stdout,
    )
}

async fn read_command_pipe<R: AsyncRead + Unpin>(
    mut reader: R,
    limit: usize,
) -> io::Result<(Vec<u8>, bool)> {
    let mut output = Vec::with_capacity(limit.min(64 << 10));
    let mut chunk = [0u8; 16 << 10];
    let mut overflow = false;
    loop {
        let read = reader.read(&mut chunk).await?;
        if read == 0 {
            break;
        }
        let keep = read.min(limit.saturating_sub(output.len()));
        output.extend_from_slice(&chunk[..keep]);
        overflow |= keep < read;
    }
    Ok((output, overflow))
}

async fn write_command_stdin(
    stdin: Option<tokio::process::ChildStdin>,
    contents: &[u8],
) -> io::Result<()> {
    if let Some(mut stdin) = stdin {
        stdin.write_all(contents).await?;
    }
    Ok(())
}

// Apply Go's per-user document ACLs to authenticated non-admin principals,
// including the synthetic legacy "worker" account. A missing ACL is the Go
// public default; malformed configured policy fails closed as required by the
// repository security contract.
fn acl_can_read_many(
    state: &AppState,
    username: &str,
    paths: &[String],
) -> std::collections::HashSet<String> {
    let Ok(root) = open_current_root(state) else {
        return std::collections::HashSet::new();
    };
    let Ok(rules) = read_acl_file::<DocumentRule>(&root, ".symdesk/permissions.json") else {
        // Go's CanReadMany fails closed when permissions.json cannot be read.
        return std::collections::HashSet::new();
    };
    let groups =
        read_acl_file::<PermissionGroup>(&root, ".symdesk/groups.json").unwrap_or_default();
    paths
        .iter()
        .filter(|path| acl_allows(&rules, &groups, username, path, false))
        .cloned()
        .collect()
}

fn acl_can_access(state: &AppState, username: &str, path: &str, write: bool) -> bool {
    let Ok(root) = open_current_root(state) else {
        return false;
    };
    let rules = read_acl_file::<DocumentRule>(&root, ".symdesk/permissions.json");
    let Ok(rules) = rules else {
        // A present but unreadable or malformed ACL is configured policy and
        // must fail closed for every non-admin principal.
        return false;
    };
    let groups =
        read_acl_file::<PermissionGroup>(&root, ".symdesk/groups.json").unwrap_or_default();
    acl_allows(&rules, &groups, username, path, write)
}

fn read_acl_file<T: for<'de> Deserialize<'de>>(
    root: &cap_std::fs::Dir,
    path: &str,
) -> Result<Vec<T>, ()> {
    let mut file = match root.open(path) {
        Ok(file) => file,
        Err(error) if error.kind() == io::ErrorKind::NotFound => return Ok(Vec::new()),
        Err(_) => return Err(()),
    };
    let mut bytes = Vec::new();
    file.read_to_end(&mut bytes).map_err(|_| ())?;
    if bytes.is_empty() {
        return Ok(Vec::new());
    }
    serde_json::from_slice(&bytes).map_err(|_| ())
}

fn acl_allows(
    rules: &[DocumentRule],
    groups: &[PermissionGroup],
    username: &str,
    path: &str,
    write: bool,
) -> bool {
    let rule = rules
        .iter()
        .find(|rule| rule.path == path)
        .or_else(|| rules.iter().find(|rule| rule.path == "*"));
    let Some(rule) = rule else {
        return true;
    };
    if rule.owner == username {
        return true;
    }
    let (users, group_names) = if write {
        (&rule.write_users, &rule.write_groups)
    } else {
        (&rule.read_users, &rule.read_groups)
    };
    users.iter().any(|user| user == username)
        || group_names.iter().any(|name| {
            groups.iter().any(|group| {
                group.name == *name && group.members.iter().any(|member| member == username)
            })
        })
}

fn validate_tokens(token: &str, worker_token: Option<&str>) -> Result<(), String> {
    if token.len() < 32 {
        return Err("server token must contain at least 32 characters".to_owned());
    }
    if let Some(worker_token) = worker_token {
        if worker_token.len() < 32 {
            return Err("worker token must contain at least 32 characters".to_owned());
        }
        if constant_time_equal(worker_token.as_bytes(), token.as_bytes()) {
            return Err("worker token must differ from the server token".to_owned());
        }
    }
    Ok(())
}

fn client_ip(request: &Request<Body>) -> String {
    request
        .extensions()
        .get::<axum::extract::ConnectInfo<SocketAddr>>()
        .map(|info| info.0.ip().to_string())
        .unwrap_or_default()
}

fn ai_rate_limit_response(state: &AppState, request: &Request<Body>) -> Option<Response> {
    let retry_after = state
        .auth_failures
        .lock()
        .ok()
        .and_then(|mut throttle| throttle.record_ai(&client_ip(request)));
    retry_after.map(|retry_after| {
        let mut response = json_error(
            StatusCode::TOO_MANY_REQUESTS,
            "too many AI requests — try again shortly",
        );
        response.headers_mut().insert(
            header::RETRY_AFTER,
            HeaderValue::from_str(&retry_after_seconds(retry_after).to_string())
                .unwrap_or_else(|_| HeaderValue::from_static("1")),
        );
        response
    })
}

impl AuthThrottle {
    fn record(&mut self, ip: &str) -> Option<Duration> {
        self.record_bucket(ip, false, false)
    }

    fn record_share(&mut self, ip: &str) -> Option<Duration> {
        self.record_bucket(ip, true, false)
    }

    fn record_ai(&mut self, ip: &str) -> Option<Duration> {
        self.record_bucket(ip, false, true)
    }

    fn record_bucket(&mut self, ip: &str, share: bool, ai: bool) -> Option<Duration> {
        let now = SystemTime::now();
        self.entries.retain(|_, failure| {
            now.duration_since(failure.last_seen)
                .map(|age| age <= Duration::from_secs(600))
                .unwrap_or(true)
        });
        if !self.entries.contains_key(ip)
            && self.entries.len() >= AUTH_MAX_ENTRIES
            && let Some(oldest) = self
                .entries
                .iter()
                .min_by_key(|(_, failure)| failure.last_seen)
                .map(|(key, _)| key.clone())
        {
            self.entries.remove(&oldest);
        }
        let failure = self
            .entries
            .entry(ip.to_owned())
            .or_insert_with(|| AuthFailure {
                auth: FailureBucket::default(),
                share: FailureBucket::default(),
                ai: FailureBucket::default(),
                last_seen: now,
            });
        failure.last_seen = now;
        let (window, max, block) = if share {
            (SHARE_WINDOW, SHARE_MAX, SHARE_BLOCK)
        } else if ai {
            (AI_WINDOW, AI_MAX, AI_BLOCK)
        } else {
            (AUTH_WINDOW, AUTH_MAX, AUTH_BLOCK)
        };
        let bucket = if share {
            &mut failure.share
        } else if ai {
            &mut failure.ai
        } else {
            &mut failure.auth
        };
        bucket.record(now, window, max, block)
    }
}

impl FailureBucket {
    fn record(
        &mut self,
        now: SystemTime,
        window: Duration,
        max: u32,
        block: Duration,
    ) -> Option<Duration> {
        if self.blocked_until > now {
            return Some(self.blocked_until.duration_since(now).unwrap_or_default());
        }
        if now
            .duration_since(self.window_start)
            .map(|age| age > window)
            .unwrap_or(true)
        {
            self.count = 0;
            self.window_start = now;
        }
        self.count = self.count.saturating_add(1);
        if self.count >= max {
            self.blocked_until = now + block;
            return Some(block);
        }
        None
    }
}

impl Default for FailureBucket {
    fn default() -> Self {
        Self {
            count: 0,
            window_start: UNIX_EPOCH,
            blocked_until: UNIX_EPOCH,
        }
    }
}

fn retry_after_seconds(duration: Duration) -> u64 {
    duration.as_secs() + u64::from(!duration.subsec_nanos().eq(&0))
}

async fn security_headers(request: Request<Body>, next: Next) -> Response {
    let mut response = next.run(request).await;
    let is_range_error = response.status() == StatusCode::RANGE_NOT_SATISFIABLE;
    let headers = response.headers_mut();
    headers.insert(
        header::X_CONTENT_TYPE_OPTIONS,
        HeaderValue::from_static("nosniff"),
    );
    headers.insert(
        header::REFERRER_POLICY,
        HeaderValue::from_static("no-referrer"),
    );
    if is_range_error {
        headers.remove(header::CACHE_CONTROL);
    } else {
        headers.insert(header::CACHE_CONTROL, HeaderValue::from_static("no-store"));
    }
    headers.insert(
        header::CONTENT_SECURITY_POLICY,
        HeaderValue::from_static("default-src 'none'; frame-ancestors 'none'"),
    );
    response
}

async fn normalize_method_not_allowed(request: Request<Body>, next: Next) -> Response {
    let path = request.uri().path().to_owned();
    let mut response = next.run(request).await;
    if response.status() == StatusCode::METHOD_NOT_ALLOWED {
        response.headers_mut().remove(header::CONTENT_TYPE);
        response.headers_mut().insert(
            header::CONTENT_TYPE,
            HeaderValue::from_static("text/plain; charset=utf-8"),
        );
        let allow = match path.as_str() {
            "/api/v1/files" => "GET, HEAD, PUT",
            "/api/v1/share" => "POST",
            "/api/v1/ingest"
            | "/api/v1/jobs/retry"
            | "/api/v1/command"
            | "/api/v1/worker/lease"
            | "/api/v1/worker/complete"
            | "/api/v1/worker/fail"
            | "/api/v1/ai/transform" => "POST",
            path if path.starts_with("/api/v1/share/") => "DELETE",
            _ => "GET, HEAD",
        };
        response
            .headers_mut()
            .insert(header::ALLOW, HeaderValue::from_static(allow));
        *response.body_mut() = Body::from(b"Method Not Allowed\n".to_vec());
    }
    response
}

async fn request_timeout(request: Request<Body>, next: Next) -> Response {
    if request.method() == Method::POST && request.uri().path() == "/api/v1/command" {
        return next.run(request).await;
    }
    match tokio::time::timeout(READ_TIMEOUT, next.run(request)).await {
        Ok(response) => response,
        Err(_) => json_error(StatusCode::REQUEST_TIMEOUT, "request timed out"),
    }
}

async fn handle_health(method: Method) -> Response {
    let body = if method == Method::HEAD {
        Vec::new()
    } else {
        br#"{"status":"ok"}"#.to_vec()
    };
    bytes_response(
        StatusCode::OK,
        vec![
            (header::CONTENT_TYPE, "application/json".to_owned()),
            (header::CONTENT_LENGTH, "15".to_owned()),
        ],
        body,
    )
}

async fn handle_status(State(state): State<Arc<AppState>>) -> Response {
    json_response(
        StatusCode::OK,
        json!({
            "capabilities": ["snapshot", "files", "ingest", "remote_worker", "command"],
            "mode": "self_hosted",
            "schema_version": 1,
            "status": "ok",
            "version": state.version,
        }),
    )
}

#[derive(Clone, Debug, Deserialize, Serialize)]
struct ShareLink {
    #[serde(default)]
    id: String,
    #[serde(default)]
    path: String,
    #[serde(default)]
    created_by: String,
    #[serde(default = "go_zero_time")]
    created_at: String,
    #[serde(default = "go_zero_time")]
    expires_at: String,
    #[serde(default)]
    token_hash: String,
    #[serde(default, skip_serializing_if = "is_false")]
    expired: bool,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    revoked_at: Option<String>,
}

#[derive(Debug, Deserialize)]
struct CreateShareRequest {
    #[serde(default)]
    path: String,
    #[serde(default)]
    expiry: i64,
}

#[derive(Serialize)]
struct CreateShareResponse {
    id: String,
    token: String,
    path: String,
    created_at: String,
    expires_at: String,
    url: String,
}

async fn handle_create_share(
    State(state): State<Arc<AppState>>,
    Extension(role): Extension<AuthRole>,
    body: Body,
) -> Response {
    if !role.has_role("user") && !role.is_admin() {
        return json_error(StatusCode::FORBIDDEN, "access denied");
    }
    let body = match to_bytes(body, MAX_SHARE_STORE_BYTES as usize).await {
        Ok(body) => body,
        Err(_) => return json_error(StatusCode::BAD_REQUEST, "invalid request body"),
    };
    let request = match serde_json::from_slice::<CreateShareRequest>(&body) {
        Ok(request) => request,
        Err(_) => return json_error(StatusCode::BAD_REQUEST, "invalid request body"),
    };
    if request.path.is_empty() {
        return json_error(StatusCode::BAD_REQUEST, "path is required");
    }
    if request.path == ".symdesk" || request.path.starts_with(".symdesk/") {
        return json_error(
            StatusCode::BAD_REQUEST,
            "internal server files are not available through the document API",
        );
    }
    let relative = match confined_path(&state, &request.path) {
        Ok(path) => path,
        Err(_) => return json_error(StatusCode::BAD_REQUEST, "a vault-relative path is required"),
    };
    let normalized = request.path.trim();
    if normalized == "datasets" || normalized.starts_with("datasets/") {
        return json_error(
            StatusCode::FORBIDDEN,
            "dataset-backed content cannot be shared",
        );
    }
    if !(1..=168).contains(&request.expiry) {
        return json_error(
            StatusCode::BAD_REQUEST,
            "expiry must be between 1 and 168 hours",
        );
    }
    if !role.is_admin() {
        let Some(username) = role.name() else {
            return json_error(StatusCode::FORBIDDEN, "access denied");
        };
        if !acl_can_access(&state, username, &normalize_snapshot_path(&relative), false) {
            return json_error(StatusCode::FORBIDDEN, "access denied");
        }
    }

    let root = match open_current_root(&state) {
        Ok(root) => root,
        Err(_) => return json_error(StatusCode::NOT_FOUND, "document not found"),
    };
    let mut options = cap_std::fs::OpenOptions::new();
    options.read(true);
    #[cfg(unix)]
    {
        use cap_std::fs::OpenOptionsExt;
        options.custom_flags(libc::O_NONBLOCK);
    }
    let file = match root.open_with(&relative, &options) {
        Ok(file) => file,
        Err(_) => return json_error(StatusCode::NOT_FOUND, "document not found"),
    };
    if !file.metadata().is_ok_and(|metadata| metadata.is_file()) {
        return json_error(StatusCode::NOT_FOUND, "document not found");
    }

    let _guard = match state.share_write.lock() {
        Ok(guard) => guard,
        Err(_) => {
            return json_error(
                StatusCode::INTERNAL_SERVER_ERROR,
                "failed to create share link",
            );
        }
    };
    if create_parent_directories(&root, Path::new(".symdesk/server")).is_err() {
        return json_error(
            StatusCode::INTERNAL_SERVER_ERROR,
            "failed to create share link",
        );
    }
    let mut links = match read_shares(&root) {
        Ok(links) => links,
        Err(()) => {
            return json_error(
                StatusCode::INTERNAL_SERVER_ERROR,
                "failed to create share link",
            );
        }
    };
    let mut id_bytes = [0_u8; 12];
    let mut token_bytes = [0_u8; 32];
    if getrandom::fill(&mut id_bytes).is_err() || getrandom::fill(&mut token_bytes).is_err() {
        return json_error(
            StatusCode::INTERNAL_SERVER_ERROR,
            "failed to create share link",
        );
    }
    let id = lowercase_hex(&id_bytes);
    let token = lowercase_hex(&token_bytes);
    let now = OffsetDateTime::now_utc();
    let created_at = now.format(&Rfc3339).unwrap_or_else(|_| go_zero_time());
    let expires_at = (now + time::Duration::hours(request.expiry))
        .format(&Rfc3339)
        .unwrap_or_else(|_| go_zero_time());
    links.push(ShareLink {
        id: id.clone(),
        path: relative.to_string_lossy().into_owned(),
        created_by: role.name().unwrap_or("admin").to_owned(),
        created_at: created_at.clone(),
        expires_at: expires_at.clone(),
        token_hash: symdesk_vault::sha256_hex(token.as_bytes()),
        expired: false,
        revoked_at: None,
    });
    let data = match serde_json::to_vec_pretty(&links) {
        Ok(data) => data,
        Err(_) => {
            return json_error(
                StatusCode::INTERNAL_SERVER_ERROR,
                "failed to create share link",
            );
        }
    };
    if write_atomic_root(
        &root,
        Path::new(".symdesk/server/shares.json"),
        &data,
        0o600,
    )
    .is_err()
    {
        return json_error(
            StatusCode::INTERNAL_SERVER_ERROR,
            "failed to create share link",
        );
    }
    json_response(
        StatusCode::CREATED,
        CreateShareResponse {
            id,
            token: token.clone(),
            path: relative.to_string_lossy().into_owned(),
            created_at,
            expires_at,
            url: format!("/s/{token}"),
        },
    )
}

fn lowercase_hex(bytes: &[u8]) -> String {
    let mut hex = String::with_capacity(bytes.len() * 2);
    for byte in bytes {
        let _ = write!(hex, "{byte:02x}");
    }
    hex
}

async fn handle_shares(
    State(state): State<Arc<AppState>>,
    Extension(role): Extension<AuthRole>,
) -> Response {
    let root = match open_current_root(&state) {
        Ok(root) => root,
        Err(error) => return json_error(StatusCode::INTERNAL_SERVER_ERROR, &error),
    };
    let mut links = match read_shares(&root) {
        Ok(links) => links,
        Err(()) => return json_error(StatusCode::INTERNAL_SERVER_ERROR, "failed to list shares"),
    };
    if !role.is_admin() {
        let username = role.name().unwrap_or_default();
        links.retain(|link| link.created_by == username);
    }
    for link in &mut links {
        link.token_hash.clear();
    }
    links.reverse();
    json_response(StatusCode::OK, links)
}

async fn handle_access_share(
    State(state): State<Arc<AppState>>,
    AxumPath(token): AxumPath<String>,
    headers: HeaderMap,
    method: Method,
    request: Request<Body>,
) -> Response {
    if token.is_empty() {
        return json_error(StatusCode::NOT_FOUND, "not found");
    }
    let link = open_current_root(&state)
        .ok()
        .and_then(|root| read_shares(&root).ok())
        .and_then(|links| lookup_share(&links, &token).ok().cloned());
    let Some(link) = link else {
        return share_lookup_failure(&state, &request);
    };
    if is_dataset_share_path(&link.path) {
        return json_error(StatusCode::NOT_FOUND, "not found");
    }
    let relative = match confined_path(&state, &link.path) {
        Ok(relative) => relative,
        Err(_) => return json_error(StatusCode::NOT_FOUND, "file not found"),
    };
    serve_vault_file(&state, &relative, &headers, method)
}

fn lookup_share<'a>(links: &'a [ShareLink], token: &str) -> Result<&'a ShareLink, ()> {
    for link in links {
        OffsetDateTime::parse(&link.created_at, &Rfc3339).map_err(|_| ())?;
        OffsetDateTime::parse(&link.expires_at, &Rfc3339).map_err(|_| ())?;
        if let Some(revoked_at) = link.revoked_at.as_deref() {
            OffsetDateTime::parse(revoked_at, &Rfc3339).map_err(|_| ())?;
        }
    }
    let target = symdesk_vault::sha256_hex(token.as_bytes());
    let now = OffsetDateTime::now_utc();
    for link in links {
        if !constant_time_equal(link.token_hash.as_bytes(), target.as_bytes()) {
            continue;
        }
        let expires_at = OffsetDateTime::parse(&link.expires_at, &Rfc3339).map_err(|_| ())?;
        if link.expired || now >= expires_at {
            return Err(());
        }
        return Ok(link);
    }
    Err(())
}

fn is_dataset_share_path(path: &str) -> bool {
    let path = path.trim();
    path == "datasets" || path.starts_with("datasets/")
}

fn share_lookup_failure(state: &AppState, request: &Request<Body>) -> Response {
    let retry_after = state
        .auth_failures
        .lock()
        .ok()
        .and_then(|mut throttle| throttle.record_share(&client_ip(request)));
    if let Some(retry_after) = retry_after {
        let mut response = json_error(
            StatusCode::TOO_MANY_REQUESTS,
            "too many share access attempts",
        );
        response.headers_mut().insert(
            header::RETRY_AFTER,
            HeaderValue::from_str(&retry_after_seconds(retry_after).to_string())
                .unwrap_or_else(|_| HeaderValue::from_static("1")),
        );
        return response;
    }
    json_error(StatusCode::NOT_FOUND, "not found")
}

fn read_shares(root: &cap_std::fs::Dir) -> Result<Vec<ShareLink>, ()> {
    let shares_dir = match root.open_dir(".symdesk/server") {
        Ok(directory) => directory,
        Err(error) if error.kind() == io::ErrorKind::NotFound => return Ok(Vec::new()),
        Err(_) => return Err(()),
    };
    let mut options = cap_std::fs::OpenOptions::new();
    options.read(true);
    #[cfg(unix)]
    {
        use cap_std::fs::OpenOptionsExt;
        options.custom_flags(libc::O_NONBLOCK | libc::O_NOFOLLOW);
    }
    let file = match shares_dir.open_with("shares.json", &options) {
        Ok(file) => file,
        Err(error) if error.kind() == io::ErrorKind::NotFound => return Ok(Vec::new()),
        Err(_) => return Err(()),
    };
    let metadata = match file.metadata() {
        Ok(metadata) if metadata.is_file() && metadata.len() <= MAX_SHARE_STORE_BYTES => metadata,
        _ => return Err(()),
    };
    let mut bytes = Vec::with_capacity(metadata.len() as usize);
    if file
        .take(MAX_SHARE_STORE_BYTES + 1)
        .read_to_end(&mut bytes)
        .is_err()
        || bytes.len() as u64 > MAX_SHARE_STORE_BYTES
    {
        return Err(());
    }
    serde_json::from_slice::<Vec<ShareLink>>(&bytes).map_err(|_| ())
}

async fn handle_revoke_share(
    State(state): State<Arc<AppState>>,
    AxumPath(id): AxumPath<String>,
    Extension(role): Extension<AuthRole>,
) -> Response {
    let _guard = match state.share_write.lock() {
        Ok(guard) => guard,
        Err(_) => return json_error(StatusCode::INTERNAL_SERVER_ERROR, "failed to revoke share"),
    };
    let root = match open_current_root(&state) {
        Ok(root) => root,
        Err(_) => {
            return json_error(
                StatusCode::NOT_FOUND,
                "share link not found or already revoked",
            );
        }
    };
    let mut links = match read_shares(&root) {
        Ok(links) => links,
        Err(()) => {
            return json_error(
                StatusCode::NOT_FOUND,
                "share link not found or already revoked",
            );
        }
    };
    if !role.is_admin()
        && !links
            .iter()
            .any(|link| Some(link.created_by.as_str()) == role.name() && link.id == id)
    {
        return json_error(
            StatusCode::NOT_FOUND,
            "share link not found or already revoked",
        );
    }
    let Some(link) = links.iter_mut().find(|link| link.id == id && !link.expired) else {
        return json_error(
            StatusCode::NOT_FOUND,
            "share link not found or already revoked",
        );
    };
    link.expired = true;
    link.revoked_at = Some(
        OffsetDateTime::now_utc()
            .format(&Rfc3339)
            .unwrap_or_else(|_| go_zero_time()),
    );
    let data = match serde_json::to_vec_pretty(&links) {
        Ok(data) => data,
        Err(_) => {
            return json_error(
                StatusCode::NOT_FOUND,
                "share link not found or already revoked",
            );
        }
    };
    if write_atomic_root(
        &root,
        Path::new(".symdesk/server/shares.json"),
        &data,
        0o600,
    )
    .is_err()
    {
        return json_error(
            StatusCode::NOT_FOUND,
            "share link not found or already revoked",
        );
    }
    json_response(StatusCode::OK, serde_json::json!({"status":"revoked"}))
}

async fn handle_notebooks(State(state): State<Arc<AppState>>) -> Response {
    let notebooks_dir = state.vault_root.join("notebooks");
    let canonical_dir = match fs::canonicalize(&notebooks_dir) {
        Ok(path) if path.starts_with(&state.vault_root) => path,
        Ok(_) => {
            return json_error(
                StatusCode::INTERNAL_SERVER_ERROR,
                "notebook directory escapes vault",
            );
        }
        Err(error) if error.kind() == io::ErrorKind::NotFound => {
            return bytes_response(
                StatusCode::OK,
                vec![
                    (header::CONTENT_TYPE, "application/json".to_owned()),
                    (header::CONTENT_LENGTH, "3".to_owned()),
                ],
                b"[]\n".as_slice(),
            );
        }
        Err(error) => return json_error(StatusCode::INTERNAL_SERVER_ERROR, &error.to_string()),
    };
    let entries = match fs::read_dir(&canonical_dir) {
        Ok(entries) => entries,
        Err(error) => return json_error(StatusCode::INTERNAL_SERVER_ERROR, &error.to_string()),
    };
    let root_dir = match open_current_root(&state) {
        Ok(root_dir) => root_dir,
        Err(error) => return json_error(StatusCode::INTERNAL_SERVER_ERROR, &error),
    };
    let mut notebooks: Vec<Notebook> = Vec::new();
    for entry in entries {
        let Ok(entry) = entry else { continue };
        let path = entry.path();
        if path.extension().and_then(|extension| extension.to_str()) != Some("md") {
            continue;
        }
        let Ok(canonical_path) = fs::canonicalize(&path) else {
            continue;
        };
        if !canonical_path.starts_with(&state.vault_root) {
            continue;
        }
        let Some(name) = path.file_name().and_then(|name| name.to_str()) else {
            continue;
        };
        let relative = format!("notebooks/{name}");
        let Ok(contents) = read_root_file(&root_dir, Path::new(&relative)) else {
            continue;
        };
        if let Ok(notebook) = parse_notebook(&relative, &contents) {
            notebooks.push(notebook);
        }
    }
    notebooks.sort_by_key(|notebook| notebook.title.to_lowercase());
    let mut body = match serde_json::to_vec(&notebooks) {
        Ok(body) => body,
        Err(error) => return json_error(StatusCode::INTERNAL_SERVER_ERROR, &error.to_string()),
    };
    body.push(b'\n');
    bytes_response(
        StatusCode::OK,
        vec![
            (header::CONTENT_TYPE, "application/json".to_owned()),
            (header::CONTENT_LENGTH, body.len().to_string()),
        ],
        body,
    )
}

async fn handle_jobs(
    State(state): State<Arc<AppState>>,
    Query(query): Query<JobQuery>,
) -> Response {
    let paged = query.limit.is_some() || query.offset.is_some();
    let limit = match query.limit.as_deref().filter(|value| !value.is_empty()) {
        Some(value) => match value.parse::<i64>() {
            Ok(value) if value > 0 => value as usize,
            _ => return json_error(StatusCode::BAD_REQUEST, "limit must be a positive integer"),
        },
        None => 100,
    };
    let offset = match query.offset.as_deref().filter(|value| !value.is_empty()) {
        Some(value) => match value.parse::<i64>() {
            Ok(value) if value >= 0 => value as usize,
            _ => {
                return json_error(
                    StatusCode::BAD_REQUEST,
                    "offset must be a non-negative integer",
                );
            }
        },
        None => 0,
    };
    let mut jobs = match read_jobs(&state) {
        Ok(jobs) => jobs,
        Err(error) => return json_error(StatusCode::INTERNAL_SERVER_ERROR, &error),
    };
    jobs.sort_by(|left, right| {
        let left = OffsetDateTime::parse(&left.created_at, &Rfc3339);
        let right = OffsetDateTime::parse(&right.created_at, &Rfc3339);
        match (left, right) {
            (Ok(left), Ok(right)) => right.cmp(&left),
            _ => std::cmp::Ordering::Equal,
        }
    });
    let total = jobs.len();
    let start = offset.min(total);
    if !paged {
        jobs.truncate(limit);
        return json_response(StatusCode::OK, jobs);
    }
    let end = start.saturating_add(limit).min(total);
    json_response(
        StatusCode::OK,
        JobPage {
            jobs: jobs.drain(start..end).collect(),
            total,
            limit,
            offset,
        },
    )
}

async fn handle_worker_input(
    State(state): State<Arc<AppState>>,
    Query(query): Query<JobQuery>,
    headers: HeaderMap,
    method: Method,
) -> Response {
    let id = query.id.as_deref().unwrap_or_default();
    if !valid_job_id(id) {
        return json_error(StatusCode::NOT_FOUND, "job not found");
    }
    let root = match open_current_root(&state) {
        Ok(root) => root,
        Err(_) => return json_error(StatusCode::NOT_FOUND, "job not found"),
    };
    let job = match read_job_record(&root, id) {
        Ok(job) => job,
        Err(_) => return json_error(StatusCode::NOT_FOUND, "job not found"),
    };
    let relative = match confined_path(&state, &job.source_path) {
        Ok(relative) => relative,
        Err(PathError::Invalid) => {
            return json_error(StatusCode::BAD_REQUEST, "a vault-relative path is required");
        }
    };
    let filename = safe_filename_value(&job.original_name);
    serve_vault_file_as(&state, &relative, &headers, method, "attachment", &filename)
}

async fn handle_worker_fail(
    State(state): State<Arc<AppState>>,
    request: Request<Body>,
) -> Response {
    let body = match to_bytes(
        request.into_body(),
        MAX_WORKER_FAIL_BODY_BYTES.saturating_add(1),
    )
    .await
    {
        Ok(body) if body.len() <= MAX_WORKER_FAIL_BODY_BYTES => body,
        _ => {
            return json_error(
                StatusCode::BAD_REQUEST,
                "invalid JSON: request body too large",
            );
        }
    };
    let request: WorkerFailRequest = match serde_json::from_slice(&body) {
        Ok(request) => request,
        Err(error) => {
            return json_error(StatusCode::BAD_REQUEST, &format!("invalid JSON: {error}"));
        }
    };
    if !valid_job_id(&request.job_id) {
        return json_error(StatusCode::CONFLICT, "invalid job id");
    }
    let _guard = match state.job_retry.lock() {
        Ok(guard) => guard,
        Err(_) => return json_error(StatusCode::INTERNAL_SERVER_ERROR, "job retry lock failed"),
    };
    let root = match open_current_root(&state) {
        Ok(root) => root,
        Err(error) => return json_error(StatusCode::CONFLICT, &error.to_string()),
    };
    let mut job = match read_job_record(&root, &request.job_id) {
        Ok(job) => job,
        Err(error) => return json_error(StatusCode::CONFLICT, &error),
    };
    if job.status != "processing" || job.worker_id != request.worker_id {
        return json_error(
            StatusCode::CONFLICT,
            &format!("job is not leased by worker {:?}", request.worker_id),
        );
    }
    job.status = if request.retry { "pending" } else { "failed" }.to_owned();
    job.error = request.error.trim().to_owned();
    job.lease_until = None;
    job.updated_at = OffsetDateTime::now_utc()
        .format(&Rfc3339)
        .unwrap_or_else(|_| go_zero_time());
    if request.retry {
        job.worker_id.clear();
    }
    let directory = match root.open_dir(".symdesk/server/jobs") {
        Ok(directory) => directory,
        Err(error) => return json_error(StatusCode::CONFLICT, &error.to_string()),
    };
    let path = PathBuf::from(format!("{}.json", request.job_id));
    let data = match serde_json::to_vec_pretty(&job) {
        Ok(data) => data,
        Err(error) => return json_error(StatusCode::INTERNAL_SERVER_ERROR, &error.to_string()),
    };
    if let Err(error) = write_atomic_root(&directory, &path, &data, 0o600) {
        return json_error(StatusCode::CONFLICT, &error.to_string());
    }
    json_response(StatusCode::OK, job)
}

async fn handle_worker_lease(
    State(state): State<Arc<AppState>>,
    request: Request<Body>,
) -> Response {
    let body = match read_capped_body(
        request.into_body(),
        MAX_WORKER_LEASE_BODY_BYTES.saturating_add(1),
    )
    .await
    {
        Ok(body) => body,
        _ => {
            return json_error(
                StatusCode::BAD_REQUEST,
                "worker_id and capabilities are required",
            );
        }
    };
    let parse_body = &body[..body
        .len()
        .min(MAX_WORKER_LEASE_BODY_BYTES.saturating_add(1))];
    let request: WorkerLeaseRequest = match decode_first_json_value(parse_body) {
        Ok(request) => request,
        Err(_) => {
            return json_error(
                StatusCode::BAD_REQUEST,
                "worker_id and capabilities are required",
            );
        }
    };
    let worker_id = request.worker_id.unwrap_or_default();
    if worker_id.trim().is_empty() {
        return json_error(
            StatusCode::BAD_REQUEST,
            "worker_id and capabilities are required",
        );
    }
    let capabilities = request.capabilities.unwrap_or_default();
    if !capabilities.iter().any(|capability| capability == "ocr") {
        return bytes_response(StatusCode::NO_CONTENT, Vec::new(), Vec::new());
    }
    let _guard = match state.job_retry.lock() {
        Ok(guard) => guard,
        Err(_) => return json_error(StatusCode::INTERNAL_SERVER_ERROR, "job retry lock failed"),
    };
    let mut jobs = match read_jobs(&state) {
        Ok(jobs) => jobs,
        Err(error) => return json_error(StatusCode::INTERNAL_SERVER_ERROR, &error),
    };
    jobs.sort_by(|left, right| {
        let left = OffsetDateTime::parse(&left.created_at, &Rfc3339).ok();
        let right = OffsetDateTime::parse(&right.created_at, &Rfc3339).ok();
        left.cmp(&right)
    });
    let now = OffsetDateTime::now_utc();
    let lease_until = (now + time::Duration::minutes(15))
        .format(&Rfc3339)
        .unwrap_or_else(|_| go_zero_time());
    let updated_at = now.format(&Rfc3339).unwrap_or_else(|_| go_zero_time());
    for mut job in jobs {
        if job.status == "processing"
            && job
                .lease_until
                .as_deref()
                .and_then(|value| OffsetDateTime::parse(value, &Rfc3339).ok())
                .is_some_and(|until| until < now)
        {
            job.status = "pending".to_owned();
            job.worker_id.clear();
            job.lease_until = None;
            job.error.clear();
        }
        if job.status != "pending" || job.capability != "ocr" {
            continue;
        }
        if !valid_job_id(&job.id) {
            return json_error(StatusCode::INTERNAL_SERVER_ERROR, "invalid job id");
        }
        job.status = "processing".to_owned();
        job.worker_id = worker_id;
        job.lease_until = Some(lease_until);
        job.updated_at = updated_at;
        let root = match open_current_root(&state) {
            Ok(root) => root,
            Err(error) => {
                return json_error(StatusCode::INTERNAL_SERVER_ERROR, &error.to_string());
            }
        };
        let directory = match root.open_dir(".symdesk/server/jobs") {
            Ok(directory) => directory,
            Err(error) => {
                return json_error(StatusCode::INTERNAL_SERVER_ERROR, &error.to_string());
            }
        };
        let path = PathBuf::from(format!("{}.json", job.id));
        let data = match serde_json::to_vec_pretty(&job) {
            Ok(data) => data,
            Err(error) => {
                return json_error(StatusCode::INTERNAL_SERVER_ERROR, &error.to_string());
            }
        };
        if let Err(error) = write_atomic_root(&directory, &path, &data, 0o600) {
            return json_error(StatusCode::INTERNAL_SERVER_ERROR, &error.to_string());
        }
        return json_response(StatusCode::OK, job);
    }
    bytes_response(StatusCode::NO_CONTENT, Vec::new(), Vec::new())
}

async fn handle_worker_complete(
    State(state): State<Arc<AppState>>,
    request: Request<Body>,
) -> Response {
    let body = match read_capped_body(
        request.into_body(),
        MAX_WORKER_COMPLETE_BODY_BYTES.saturating_add(1),
    )
    .await
    {
        Ok(body) => body,
        Err(error) => {
            return json_error(StatusCode::BAD_REQUEST, &format!("invalid JSON: {error}"));
        }
    };
    let parse_body = &body[..body
        .len()
        .min(MAX_WORKER_COMPLETE_BODY_BYTES.saturating_add(1))];
    let request: WorkerCompleteRequest = match decode_first_json_value(parse_body) {
        Ok(request) => request,
        Err(error) => {
            let message = if error.is_eof() {
                "unexpected EOF".to_owned()
            } else {
                error.to_string()
            };
            return json_error(StatusCode::BAD_REQUEST, &format!("invalid JSON: {message}"));
        }
    };
    if !valid_job_id(&request.job_id) {
        return json_error(StatusCode::NOT_FOUND, "job not found");
    }
    let _guard = match state.job_retry.lock() {
        Ok(guard) => guard,
        Err(_) => return json_error(StatusCode::INTERNAL_SERVER_ERROR, "job retry lock failed"),
    };
    let root = match open_current_root(&state) {
        Ok(root) => root,
        Err(_) => return json_error(StatusCode::NOT_FOUND, "job not found"),
    };
    let mut job = match read_job_record(&root, &request.job_id) {
        Ok(job) => job,
        Err(_) => return json_error(StatusCode::NOT_FOUND, "job not found"),
    };
    if job.status != "processing" || job.worker_id != request.worker_id {
        return json_error(StatusCode::CONFLICT, "job is not leased by this worker");
    }
    let note_path = match write_completed_note(&state, &root, &job, &request) {
        Ok(path) => path,
        Err(error) => return json_error(StatusCode::INTERNAL_SERVER_ERROR, &error),
    };
    let now = OffsetDateTime::now_utc();
    job.status = "completed".to_owned();
    job.engine = request.engine;
    job.model = request.model;
    job.note_path = note_path;
    job.lease_until = None;
    job.error.clear();
    job.updated_at = now.format(&Rfc3339).unwrap_or_else(|_| go_zero_time());
    let directory = match root.open_dir(".symdesk/server/jobs") {
        Ok(directory) => directory,
        Err(error) => return json_error(StatusCode::CONFLICT, &error.to_string()),
    };
    let path = PathBuf::from(format!("{}.json", job.id));
    let data = match serde_json::to_vec_pretty(&job) {
        Ok(data) => data,
        Err(error) => return json_error(StatusCode::INTERNAL_SERVER_ERROR, &error.to_string()),
    };
    if let Err(error) = write_atomic_root(&directory, &path, &data, 0o600) {
        return json_error(StatusCode::CONFLICT, &error.to_string());
    }
    json_response(StatusCode::OK, job)
}

fn write_completed_note(
    state: &AppState,
    root: &cap_std::fs::Dir,
    job: &JobRecord,
    request: &WorkerCompleteRequest,
) -> Result<String, String> {
    let extension = Path::new(&job.original_name)
        .extension()
        .and_then(|extension| extension.to_str())
        .map(|extension| format!(".{extension}"))
        .unwrap_or_default();
    let base = job
        .original_name
        .strip_suffix(&extension)
        .unwrap_or(&job.original_name)
        .chars()
        .map(|character| {
            if character == '/' || character == '\\' || character < '\u{20}' {
                '-'
            } else {
                character
            }
        })
        .collect::<String>();
    let base = base.trim_matches([' ', '.']);
    let base = if base.is_empty() { "Document" } else { base };
    let relative = confined_path(state, &format!("inbox/{base}-{}.md", &job.id[..8]))
        .map_err(|_| "a vault-relative path is required".to_owned())?;
    if let Some(parent) = relative
        .parent()
        .filter(|parent| !parent.as_os_str().is_empty())
    {
        create_parent_directories(root, parent).map_err(|error| error.to_string())?;
    }
    let created = OffsetDateTime::now_utc()
        .replace_nanosecond(0)
        .unwrap_or_else(|_| OffsetDateTime::now_utc())
        .format(&Rfc3339)
        .map_err(|error| error.to_string())?;
    let frontmatter = format!(
        "archive_path: {}\nconfidence: 0\ncreated: \"{}\"\nocr_engine: {}\nocr_model: {}\nstatus: needs_review\ntitle: {}\n",
        yaml_scalar(&job.source_path),
        created,
        yaml_scalar(&request.engine),
        yaml_scalar(&request.model),
        yaml_scalar(base),
    );
    let source_path = job.source_path.replace('\\', "/");
    let content = format!(
        "---\n{frontmatter}---\n\n![[{source_path}]]\n\n## OCR text\n\n{}\n",
        request.text.trim()
    );
    if let Err(error) = write_atomic_root(root, &relative, content.as_bytes(), 0o644) {
        return Err(error.to_string());
    }
    let file_path = state.vault_root.join(&relative);
    let Some(file_key) = file_path.to_str() else {
        return Err("document path is not valid UTF-8".to_owned());
    };
    let document = parse_bytes(file_key, content.as_bytes()).map_err(|error| error.to_string())?;
    let indexed =
        IndexedDocument::from_vault(&document, None).map_err(|error| error.to_string())?;
    let sidecar_path = state
        .vault_root
        .join(".symdesk")
        .join("server")
        .join("sidecar.db");
    let mut sidecar = Sidecar::open(&sidecar_path).map_err(|error| error.to_string())?;
    sidecar
        .index_document(&indexed)
        .map_err(|error| error.to_string())?;
    Ok(relative.to_string_lossy().replace('\\', "/"))
}

fn yaml_scalar(value: &str) -> String {
    if value.is_empty() {
        return "\"\"".to_owned();
    }
    if value.contains('\n')
        && !value
            .chars()
            .any(|character| character.is_control() && character != '\n')
    {
        let trailing_newlines = value
            .chars()
            .rev()
            .take_while(|character| *character == '\n')
            .count();
        let body = value.trim_end_matches('\n');
        let chomping = match trailing_newlines {
            0 => "-",
            1 => "",
            _ => "+",
        };
        let mut scalar = format!("|{chomping}\n");
        for line in body.split('\n') {
            scalar.push_str("    ");
            scalar.push_str(line);
            scalar.push('\n');
        }
        return scalar;
    }
    if yaml_single_quoted(value) {
        return format!("'{}'", value.replace('\'', "''"));
    }
    if value.chars().any(char::is_control) || yaml_resolves_to_non_string(value) {
        return serde_json::to_string(value).unwrap_or_else(|_| "\"\"".to_owned());
    }
    value.to_owned()
}

fn yaml_single_quoted(value: &str) -> bool {
    value.trim() != value
        || value == "-"
        || value == "..."
        || value == "---"
        || value.starts_with('#')
        || value.contains(": ")
        || value.contains(" #")
}

fn yaml_resolves_to_non_string(value: &str) -> bool {
    let lowercase = value.to_ascii_lowercase();
    if matches!(
        lowercase.as_str(),
        "null" | "~" | "true" | "false" | "yes" | "no" | "on" | "off"
    ) || matches!(lowercase.as_str(), ".inf" | "+.inf" | "-.inf" | ".nan")
    {
        return true;
    }
    if value.parse::<i64>().is_ok() || value.parse::<f64>().is_ok() {
        return true;
    }
    let bytes = value.as_bytes();
    bytes.len() >= 10
        && bytes[0..4].iter().all(u8::is_ascii_digit)
        && bytes[4] == b'-'
        && bytes[5..7].iter().all(u8::is_ascii_digit)
        && bytes[7] == b'-'
        && bytes[8..10].iter().all(u8::is_ascii_digit)
}

fn decode_first_json_value<T>(bytes: &[u8]) -> Result<T, serde_json::Error>
where
    T: for<'de> Deserialize<'de>,
{
    let mut deserializer = serde_json::Deserializer::from_slice(bytes);
    T::deserialize(&mut deserializer)
}

fn deserialize_null_string<'de, D>(deserializer: D) -> Result<String, D::Error>
where
    D: serde::Deserializer<'de>,
{
    Ok(Option::<String>::deserialize(deserializer)?.unwrap_or_default())
}

async fn read_capped_body(body: Body, limit: usize) -> Result<Vec<u8>, String> {
    let mut stream = Box::pin(body.into_data_stream());
    let mut bytes = Vec::with_capacity(limit.min(8 << 10));
    while bytes.len() < limit {
        let next = poll_fn(|context| stream.as_mut().poll_next(context)).await;
        match next {
            Some(Ok(chunk)) => {
                let remaining = limit - bytes.len();
                bytes.extend_from_slice(&chunk[..chunk.len().min(remaining)]);
            }
            Some(Err(error)) => return Err(error.to_string()),
            None => break,
        }
    }
    Ok(bytes)
}

async fn handle_ingest(
    State(state): State<Arc<AppState>>,
    multipart: Result<Multipart, axum::extract::multipart::MultipartRejection>,
) -> Response {
    let mut multipart = match multipart {
        Ok(multipart) => multipart,
        Err(_) => {
            return json_error(
                StatusCode::PAYLOAD_TOO_LARGE,
                "upload exceeds 100 MiB or is invalid",
            );
        }
    };
    let root = match open_current_root(&state) {
        Ok(root) => root,
        Err(error) => return json_error(StatusCode::INTERNAL_SERVER_ERROR, &error),
    };
    let mut upload: Option<(PathBuf, PathBuf, String, String)> = None;
    loop {
        let mut field = match multipart.next_field().await {
            Ok(Some(field)) => field,
            Ok(None) => break,
            Err(_error) => {
                if let Some((temporary, _, _, _)) = &upload {
                    let _ = root.remove_file(temporary);
                }
                return json_error(
                    StatusCode::PAYLOAD_TOO_LARGE,
                    "upload exceeds 100 MiB or is invalid",
                );
            }
        };
        if upload.is_some() || field.name() != Some("file") {
            continue;
        }
        let Some(original_name) = field.file_name().map(str::to_owned) else {
            return json_error(
                StatusCode::BAD_REQUEST,
                "multipart field 'file' is required",
            );
        };
        let name = safe_upload_filename(&original_name);
        let content_type = field
            .headers()
            .get(header::CONTENT_TYPE)
            .and_then(|value| value.to_str().ok())
            .map(str::to_owned)
            .unwrap_or_default();
        let archive_id = match new_job_id() {
            Ok(id) => id,
            Err(error) => return json_error(StatusCode::INTERNAL_SERVER_ERROR, &error),
        };
        let now = OffsetDateTime::now_utc();
        let relative = PathBuf::from(format!(
            "archive/{:04}/{:02}/{archive_id}-{name}",
            now.year(),
            u8::from(now.month())
        ));
        if confined_path(&state, relative.to_str().unwrap_or_default()).is_err() {
            return json_error(StatusCode::BAD_REQUEST, "invalid upload path");
        }
        let parent = relative.parent().expect("archive path has a parent");
        if let Err(error) = create_parent_directories(&root, parent) {
            return json_error(StatusCode::INTERNAL_SERVER_ERROR, &error.to_string());
        }
        let temporary = parent.join(format!(".upload-{archive_id}.tmp"));
        let mut options = cap_std::fs::OpenOptions::new();
        options.write(true).create_new(true);
        #[cfg(unix)]
        {
            use cap_std::fs::OpenOptionsExt;
            options.mode(0o640);
        }
        let mut file = match root.open_with(&temporary, &options) {
            Ok(file) => file,
            Err(error) => {
                return json_error(StatusCode::INTERNAL_SERVER_ERROR, &error.to_string());
            }
        };
        let mut size = 0_u64;
        loop {
            let chunk = match field.chunk().await {
                Ok(Some(chunk)) => chunk,
                Ok(None) => break,
                Err(_error) => {
                    drop(file);
                    let _ = root.remove_file(&temporary);
                    return json_error(
                        StatusCode::PAYLOAD_TOO_LARGE,
                        "upload exceeds 100 MiB or is invalid",
                    );
                }
            };
            size = size.saturating_add(chunk.len() as u64);
            if size > MAX_UPLOAD_BYTES {
                drop(file);
                let _ = root.remove_file(&temporary);
                return json_error(StatusCode::INTERNAL_SERVER_ERROR, "upload exceeds 100 MiB");
            }
            if let Err(error) = file.write_all(&chunk) {
                drop(file);
                let _ = root.remove_file(&temporary);
                return json_error(StatusCode::INTERNAL_SERVER_ERROR, &error.to_string());
            }
        }
        if let Err(error) = file.sync_all() {
            drop(file);
            let _ = root.remove_file(&temporary);
            return json_error(StatusCode::INTERNAL_SERVER_ERROR, &error.to_string());
        }
        drop(file);
        upload = Some((temporary, relative, name, content_type));
    }
    let Some((temporary, relative, name, content_type)) = upload else {
        return json_error(
            StatusCode::BAD_REQUEST,
            "multipart field 'file' is required",
        );
    };
    if let Err(error) = root.rename(&temporary, &root, &relative) {
        let _ = root.remove_file(&temporary);
        return json_error(StatusCode::INTERNAL_SERVER_ERROR, &error.to_string());
    }
    let id = match new_job_id() {
        Ok(id) => id,
        Err(error) => {
            let _ = root.remove_file(&relative);
            return json_error(StatusCode::INTERNAL_SERVER_ERROR, &error);
        }
    };
    let job_dir = Path::new(".symdesk/server/jobs");
    let mut builder = cap_std::fs::DirBuilder::new();
    builder.recursive(true);
    #[cfg(unix)]
    {
        use cap_std::fs::DirBuilderExt;
        builder.mode(0o700);
    }
    if let Err(error) = root.create_dir_with(job_dir, &builder) {
        let _ = root.remove_file(&relative);
        return json_error(StatusCode::INTERNAL_SERVER_ERROR, &error.to_string());
    }
    let timestamp = format_rfc3339(SystemTime::now());
    let job = JobRecord {
        id: id.clone(),
        schema_version: 1,
        status: "pending".to_owned(),
        source_path: relative.to_string_lossy().replace('\\', "/"),
        original_name: name,
        content_type,
        capability: "ocr".to_owned(),
        worker_id: String::new(),
        engine: String::new(),
        model: String::new(),
        note_path: String::new(),
        error: String::new(),
        created_at: timestamp.clone(),
        updated_at: timestamp,
        lease_until: None,
    };
    let job_path = job_dir.join(format!("{id}.json"));
    let data = match serde_json::to_vec_pretty(&job) {
        Ok(data) => data,
        Err(error) => {
            let _ = root.remove_file(&relative);
            return json_error(StatusCode::INTERNAL_SERVER_ERROR, &error.to_string());
        }
    };
    if let Err(error) = write_atomic_root(&root, &job_path, &data, 0o600) {
        let _ = root.remove_file(&relative);
        return json_error(StatusCode::INTERNAL_SERVER_ERROR, &error.to_string());
    }
    json_response(StatusCode::ACCEPTED, job)
}

fn new_job_id() -> Result<String, String> {
    let mut bytes = [0_u8; 16];
    getrandom::fill(&mut bytes).map_err(|error| format!("create job id: {error}"))?;
    let mut id = String::with_capacity(32);
    for byte in bytes {
        let _ = write!(id, "{byte:02x}");
    }
    Ok(id)
}

fn safe_upload_filename(name: &str) -> String {
    let name = name.rsplit(['/', '\\']).next().unwrap_or_default().trim();
    if name.is_empty() || name == "." {
        "document.bin".to_owned()
    } else {
        name.to_owned()
    }
}

async fn handle_retry_job(
    State(state): State<Arc<AppState>>,
    Query(query): Query<JobRetryQuery>,
) -> Response {
    let _guard = match state.job_retry.lock() {
        Ok(guard) => guard,
        Err(_) => return json_error(StatusCode::INTERNAL_SERVER_ERROR, "job retry lock failed"),
    };
    let Some(id) = query.id.as_deref().filter(|id| valid_job_id(id)) else {
        return json_error(StatusCode::CONFLICT, "invalid job id");
    };
    let root = match open_current_root(&state) {
        Ok(root) => root,
        Err(error) => return json_error(StatusCode::INTERNAL_SERVER_ERROR, &error.to_string()),
    };
    let jobs = match root.open_dir(".symdesk/server/jobs") {
        Ok(jobs) => jobs,
        Err(error) if error.kind() == io::ErrorKind::NotFound => {
            return json_error(StatusCode::CONFLICT, "job not found");
        }
        Err(error) => return json_error(StatusCode::INTERNAL_SERVER_ERROR, &error.to_string()),
    };
    let relative = PathBuf::from(format!("{id}.json"));
    let mut options = cap_std::fs::OpenOptions::new();
    options.read(true);
    #[cfg(unix)]
    {
        use cap_std::fs::OpenOptionsExt;
        options.custom_flags(libc::O_NONBLOCK);
    }
    let file = match jobs.open_with(&relative, &options) {
        Ok(file) => file,
        Err(error) if error.kind() == io::ErrorKind::NotFound => {
            return json_error(StatusCode::CONFLICT, "job not found");
        }
        Err(error) => return json_error(StatusCode::INTERNAL_SERVER_ERROR, &error.to_string()),
    };
    let metadata = match file.metadata() {
        Ok(metadata) if metadata.is_file() && metadata.len() <= 1 << 20 => metadata,
        Ok(metadata) if metadata.len() > 1 << 20 => {
            return json_error(StatusCode::INTERNAL_SERVER_ERROR, "job file exceeds 1 MiB");
        }
        Ok(_) => {
            return json_error(
                StatusCode::INTERNAL_SERVER_ERROR,
                "job file is not a regular file",
            );
        }
        Err(error) => return json_error(StatusCode::INTERNAL_SERVER_ERROR, &error.to_string()),
    };
    let mut bytes = Vec::with_capacity(metadata.len() as usize);
    if let Err(error) = file.take((1 << 20) + 1).read_to_end(&mut bytes) {
        return json_error(StatusCode::INTERNAL_SERVER_ERROR, &error.to_string());
    }
    if bytes.len() > 1 << 20 {
        return json_error(StatusCode::INTERNAL_SERVER_ERROR, "job file exceeds 1 MiB");
    }
    let mut job: JobRecord = match serde_json::from_slice(&bytes) {
        Ok(job) => job,
        Err(error) => {
            return json_error(StatusCode::CONFLICT, &format!("decode job {id}: {error}"));
        }
    };
    if OffsetDateTime::parse(&job.created_at, &Rfc3339).is_err()
        || OffsetDateTime::parse(&job.updated_at, &Rfc3339).is_err()
        || job
            .lease_until
            .as_deref()
            .is_some_and(|value| OffsetDateTime::parse(value, &Rfc3339).is_err())
    {
        return json_error(StatusCode::CONFLICT, "invalid job timestamp");
    }
    if job.status != "failed" {
        return json_error(StatusCode::CONFLICT, "only failed jobs can be retried");
    }
    job.status = "pending".to_owned();
    job.worker_id.clear();
    job.lease_until = None;
    job.error.clear();
    job.updated_at = OffsetDateTime::now_utc()
        .format(&Rfc3339)
        .unwrap_or_else(|_| go_zero_time());
    let data = match serde_json::to_vec_pretty(&job) {
        Ok(data) => data,
        Err(error) => return json_error(StatusCode::INTERNAL_SERVER_ERROR, &error.to_string()),
    };
    if let Err(error) = write_atomic_root(&jobs, &relative, &data, 0o600) {
        return json_error(StatusCode::INTERNAL_SERVER_ERROR, &error.to_string());
    }
    json_response(StatusCode::OK, job)
}

fn valid_job_id(id: &str) -> bool {
    id.len() == 32 && id.bytes().all(|byte| byte.is_ascii_hexdigit())
}

fn read_job_record(root: &cap_std::fs::Dir, id: &str) -> Result<JobRecord, String> {
    let directory = root
        .open_dir(".symdesk/server/jobs")
        .map_err(|error| error.to_string())?;
    let mut options = cap_std::fs::OpenOptions::new();
    options.read(true);
    #[cfg(unix)]
    {
        use cap_std::fs::OpenOptionsExt;
        options.custom_flags(libc::O_NONBLOCK);
    }
    let name = format!("{id}.json");
    let file = directory
        .open_with(&name, &options)
        .map_err(|error| error.to_string())?;
    let metadata = file.metadata().map_err(|error| error.to_string())?;
    if !metadata.is_file() {
        return Err("job file is not a regular file".to_owned());
    }
    if metadata.len() > 1 << 20 {
        return Err("job file exceeds 1 MiB".to_owned());
    }
    let mut bytes = Vec::with_capacity(metadata.len() as usize);
    file.take((1 << 20) + 1)
        .read_to_end(&mut bytes)
        .map_err(|error| error.to_string())?;
    if bytes.len() > 1 << 20 {
        return Err("job file exceeds 1 MiB".to_owned());
    }
    serde_json::from_slice(&bytes).map_err(|error| error.to_string())
}

fn read_jobs(state: &AppState) -> Result<Vec<JobRecord>, String> {
    let root = open_current_root(state).map_err(|error| error.to_string())?;
    let directory = match root.open_dir(".symdesk/server/jobs") {
        Ok(directory) => directory,
        Err(error) if error.kind() == io::ErrorKind::NotFound => return Ok(Vec::new()),
        Err(error) => return Err(format!("read job store: {error}")),
    };
    let entries = directory
        .read_dir(".")
        .map_err(|error| format!("read job store: {error}"))?;
    let mut names = Vec::new();
    for entry in entries {
        let entry = entry.map_err(|error| format!("read job store: {error}"))?;
        if entry
            .file_type()
            .map_err(|error| error.to_string())?
            .is_dir()
        {
            continue;
        }
        let name = entry.file_name();
        let Some(name) = name.to_str() else { continue };
        if !name.ends_with(".json") {
            continue;
        }
        let id = &name[..name.len() - ".json".len()];
        if id.len() != 32 || !id.bytes().all(|byte| byte.is_ascii_hexdigit()) {
            return Err("invalid job id".to_owned());
        }
        names.push(name.to_owned());
    }
    // Go's os.ReadDir sorts names. Keep that order when creation timestamps tie.
    names.sort();
    let mut jobs = Vec::with_capacity(names.len());
    for name in names {
        let mut options = cap_std::fs::OpenOptions::new();
        options.read(true);
        #[cfg(unix)]
        {
            use cap_std::fs::OpenOptionsExt;
            options.custom_flags(libc::O_NONBLOCK);
        }
        let file = directory
            .open_with(&name, &options)
            .map_err(|error| format!("{error}"))?;
        let metadata = file.metadata().map_err(|error| error.to_string())?;
        if !metadata.is_file() {
            return Err("job file is not a regular file".to_owned());
        }
        if metadata.len() > 1 << 20 {
            return Err("job file exceeds 1 MiB".to_owned());
        }
        let mut bytes = Vec::with_capacity(metadata.len() as usize);
        file.take((1 << 20) + 1)
            .read_to_end(&mut bytes)
            .map_err(|error| error.to_string())?;
        if bytes.len() > 1 << 20 {
            return Err("job file exceeds 1 MiB".to_owned());
        }
        let job: JobRecord = serde_json::from_slice(&bytes)
            .map_err(|error| format!("decode job {}: {error}", &name[..name.len() - 5]))?;
        OffsetDateTime::parse(&job.created_at, &Rfc3339)
            .map_err(|error| format!("decode job created_at: {error}"))?;
        OffsetDateTime::parse(&job.updated_at, &Rfc3339)
            .map_err(|error| format!("decode job updated_at: {error}"))?;
        if let Some(lease_until) = job.lease_until.as_deref() {
            OffsetDateTime::parse(lease_until, &Rfc3339)
                .map_err(|error| format!("decode job lease_until: {error}"))?;
        }
        jobs.push(job);
    }
    Ok(jobs)
}

fn go_zero_time() -> String {
    "0001-01-01T00:00:00Z".to_owned()
}

#[derive(Serialize)]
struct NotebookResponse {
    id: String,
    path: String,
    title: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    description: String,
    created: String,
    sources: Vec<NotebookSource>,
}

#[derive(Serialize)]
struct NotebookSource {
    path: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    title: String,
    #[serde(skip_serializing_if = "is_false")]
    missing: bool,
}

fn is_false(value: &bool) -> bool {
    !value
}

async fn handle_notebook(
    State(state): State<Arc<AppState>>,
    Extension(role): Extension<AuthRole>,
    AxumPath(id): AxumPath<String>,
) -> Response {
    let reference = id.trim();
    if reference.is_empty() {
        return json_error(StatusCode::BAD_REQUEST, "notebook id is required");
    }

    let mut relative = reference.to_owned();
    if !relative.ends_with(".md") {
        relative.push_str(".md");
    }
    if !relative.starts_with("notebooks/") {
        let Some(name) = Path::new(&relative).file_name() else {
            return json_error(StatusCode::NOT_FOUND, "notebook not found");
        };
        relative = format!("notebooks/{}", name.to_string_lossy());
    }
    let root_dir = match open_current_root(&state) {
        Ok(root_dir) => root_dir,
        Err(_) => return json_error(StatusCode::NOT_FOUND, "notebook not found"),
    };
    match secure_path(&state.vault_root, &relative) {
        Ok(_) => {}
        Err(_) => return json_error(StatusCode::NOT_FOUND, "notebook not found"),
    }
    let contents = match read_root_file(&root_dir, Path::new(&relative)) {
        Ok(contents) => contents,
        Err(error) if error.kind() == io::ErrorKind::NotFound => {
            return json_error(
                StatusCode::NOT_FOUND,
                "notebook not found: notebook not found",
            );
        }
        Err(_) => return json_error(StatusCode::NOT_FOUND, "notebook not found"),
    };
    let notebook = match parse_notebook(&relative, &contents) {
        Ok(notebook) => notebook,
        Err(_) => return json_error(StatusCode::NOT_FOUND, "notebook not found"),
    };

    let source_paths = notebook.sources.clone();
    let allowed = if role.is_admin() {
        None
    } else {
        role.name()
            .map(|name| acl_can_read_many(&state, name, &source_paths))
    };
    let sources = notebook
        .sources
        .iter()
        .filter(|path| {
            allowed
                .as_ref()
                .is_none_or(|allowed| allowed.contains(*path))
        })
        .map(|path| {
            let source = secure_path(&state.vault_root, path).ok().and_then(|_| {
                read_root_file(&root_dir, Path::new(path))
                    .ok()
                    .and_then(|bytes| parse_bytes(path, &bytes).ok())
            });
            let missing = source.is_none();
            NotebookSource {
                path: path.clone(),
                title: source.map_or_else(String::new, |document| document.title),
                missing,
            }
        })
        .collect();

    let mut body = match serde_json::to_vec(&NotebookResponse {
        id: notebook.id,
        path: notebook.path,
        title: notebook.title,
        description: notebook.description,
        created: notebook.created,
        sources,
    }) {
        Ok(body) => body,
        Err(error) => return json_error(StatusCode::INTERNAL_SERVER_ERROR, &error.to_string()),
    };
    body.push(b'\n');
    bytes_response(
        StatusCode::OK,
        vec![
            (header::CONTENT_TYPE, "application/json".to_owned()),
            (header::CONTENT_LENGTH, body.len().to_string()),
        ],
        body,
    )
}

async fn handle_snapshot(
    State(state): State<Arc<AppState>>,
    Extension(role): Extension<AuthRole>,
    headers: HeaderMap,
    method: Method,
) -> Response {
    let payload = match state.snapshot_cache.get_or_build(
        || current_root_identity(&state),
        || snapshot_payload(&state),
    ) {
        Ok(value) => value,
        Err(error) if error == SNAPSHOT_TOO_LARGE => {
            return json_error(StatusCode::PAYLOAD_TOO_LARGE, SNAPSHOT_TOO_LARGE);
        }
        Err(error) => return json_error(StatusCode::INTERNAL_SERVER_ERROR, &error),
    };
    let filtered_payload = if role.is_admin() {
        None
    } else if let Some(username) = role.name() {
        match filtered_snapshot_payload(&state, &payload, username) {
            Ok(payload) => Some(payload),
            Err(error) => return json_error(StatusCode::INTERNAL_SERVER_ERROR, &error),
        }
    } else {
        None
    };
    let payload = filtered_payload.as_ref().unwrap_or(payload.as_ref());
    let SnapshotPayload {
        plain,
        compressed,
        etag,
    } = payload;
    let quoted = format!("\"{etag}\"");
    let mut common = vec![(header::ETAG, quoted.clone())];
    if headers
        .get(header::IF_NONE_MATCH)
        .and_then(|value| value.to_str().ok())
        == Some(quoted.as_str())
    {
        return bytes_response(StatusCode::NOT_MODIFIED, common, Vec::new());
    }
    common.push((header::CONTENT_TYPE, "application/json".to_owned()));
    let gzip = headers
        .get(header::ACCEPT_ENCODING)
        .and_then(|value| value.to_str().ok())
        .is_some_and(|value| value.contains("gzip"));
    if gzip {
        common.push((header::CONTENT_ENCODING, "gzip".to_owned()));
        common.push((header::CONTENT_LENGTH, compressed.len().to_string()));
        return bytes_response(
            StatusCode::OK,
            common,
            if method == Method::HEAD {
                axum::body::Bytes::new()
            } else {
                compressed.clone()
            },
        );
    }
    common.push((header::CONTENT_LENGTH, plain.len().to_string()));
    if method == Method::HEAD {
        return bytes_response(StatusCode::OK, common, Vec::new());
    }
    bytes_response(StatusCode::OK, common, plain.clone())
}

fn filtered_snapshot_payload(
    state: &AppState,
    payload: &SnapshotPayload,
    username: &str,
) -> Result<SnapshotPayload, String> {
    let mut snapshot: Snapshot = serde_json::from_slice(&payload.plain)
        .map_err(|error| format!("invalid cached snapshot: {error}"))?;
    let paths = snapshot
        .notes
        .iter()
        .map(|note| note.path.clone())
        .collect::<Vec<_>>();
    let allowed = acl_can_read_many(state, username, &paths);
    snapshot.notes.retain(|note| allowed.contains(&note.path));
    let mut plain = serde_json::to_vec(&WorkerSnapshot {
        notes: snapshot.notes,
        generated_at: snapshot.generated_at,
    })
    .map_err(|error| error.to_string())?;
    plain.push(b'\n');
    if plain.len() as u64 > MAX_SNAPSHOT_BYTES {
        return Err(SNAPSHOT_TOO_LARGE.to_owned());
    }
    let mut encoder = GzEncoder::new(Vec::new(), Compression::default());
    encoder
        .write_all(&plain)
        .map_err(|error| error.to_string())?;
    let compressed = encoder.finish().map_err(|error| error.to_string())?;
    if compressed.len() as u64 > MAX_SNAPSHOT_BYTES {
        return Err(SNAPSHOT_TOO_LARGE.to_owned());
    }
    Ok(SnapshotPayload {
        plain: plain.into(),
        compressed: compressed.into(),
        etag: format!("{}:{username}", payload.etag),
    })
}

async fn handle_file(
    State(state): State<Arc<AppState>>,
    Extension(role): Extension<AuthRole>,
    Query(query): Query<FileQuery>,
    headers: HeaderMap,
    method: Method,
) -> Response {
    let requested = query.path.as_deref().unwrap_or_default();
    if requested == ".symdesk" || requested.starts_with(".symdesk/") {
        return json_error(
            StatusCode::BAD_REQUEST,
            "internal server files are not available through the document API",
        );
    }
    let relative = match confined_path(&state, requested) {
        Ok(path) => path,
        Err(PathError::Invalid) => {
            return json_error(StatusCode::BAD_REQUEST, "a vault-relative path is required");
        }
    };
    if !role.is_admin()
        && role.name().is_some_and(|username| {
            !acl_can_access(&state, username, &normalize_snapshot_path(&relative), false)
        })
    {
        return json_error(StatusCode::FORBIDDEN, "access denied");
    }
    serve_vault_file(&state, &relative, &headers, method)
}

fn serve_vault_file(
    state: &AppState,
    relative: &Path,
    headers: &HeaderMap,
    method: Method,
) -> Response {
    let filename = safe_filename(relative);
    serve_vault_file_as(state, relative, headers, method, "inline", &filename)
}

fn serve_vault_file_as(
    state: &AppState,
    relative: &Path,
    headers: &HeaderMap,
    method: Method,
    disposition: &str,
    filename: &str,
) -> Response {
    let root_dir = match open_current_root(state) {
        Ok(root_dir) => root_dir,
        Err(_) => return json_error(StatusCode::NOT_FOUND, "file not found"),
    };
    let mut file = match root_dir.open(relative) {
        Ok(file) => file,
        Err(_) => return json_error(StatusCode::NOT_FOUND, "file not found"),
    };
    let metadata = match file.metadata() {
        Ok(metadata) if metadata.is_file() => metadata,
        _ => return json_error(StatusCode::NOT_FOUND, "file not found"),
    };
    let length = metadata.len();
    let modified = metadata
        .modified()
        .map(|value| value.into_std())
        .unwrap_or(UNIX_EPOCH);
    let last_modified = fmt_http_date(modified);
    let sample = match read_sample(&mut file, length) {
        Ok(sample) => sample,
        Err(_) => return json_error(StatusCode::NOT_FOUND, "file not found"),
    };
    let disposition = format!("{disposition}; filename={filename:?}");
    let mut common = vec![
        (header::CONTENT_TYPE, content_type(relative, &sample)),
        (header::CONTENT_DISPOSITION, disposition.clone()),
        (header::ACCEPT_RANGES, "bytes".to_owned()),
        (header::LAST_MODIFIED, last_modified.clone()),
    ];
    if let Some(value) = headers
        .get(header::IF_MODIFIED_SINCE)
        .and_then(|value| value.to_str().ok())
        .and_then(|value| parse_http_date(value).ok())
        && modified <= value
    {
        return bytes_response(
            StatusCode::NOT_MODIFIED,
            vec![
                (header::CONTENT_DISPOSITION, disposition),
                (header::LAST_MODIFIED, last_modified),
            ],
            Vec::new(),
        );
    }

    let if_range_matches = headers.get(header::IF_RANGE).is_none_or(|value| {
        value
            .to_str()
            .ok()
            .and_then(|value| parse_http_date(value).ok())
            .is_some_and(|value| fmt_http_date(value) == last_modified)
    });
    let range = if length == 0 || !if_range_matches {
        None
    } else {
        match headers
            .get(header::RANGE)
            .and_then(|value| value.to_str().ok())
            .map(|value| parse_range(value, length))
        {
            Some(Ok(range)) => Some(range),
            Some(Err(RangeError::Invalid)) => {
                return range_error_response(relative, "invalid range", None);
            }
            Some(Err(RangeError::NoOverlap)) => {
                return range_error_response(
                    relative,
                    "invalid range: failed to overlap",
                    Some(format!("bytes */{length}")),
                );
            }
            None => None,
        }
    };
    let (status, body_length, content_range) = if let Some((start, end)) = range {
        (
            StatusCode::PARTIAL_CONTENT,
            end - start + 1,
            Some(format!("bytes {start}-{end}/{length}")),
        )
    } else {
        (StatusCode::OK, length, None)
    };
    if let Some(content_range) = content_range {
        common.push((header::CONTENT_RANGE, content_range));
    }
    common.push((header::CONTENT_LENGTH, body_length.to_string()));
    if method == Method::HEAD {
        return bytes_response(status, common, Vec::new());
    }
    let start = range.map_or(0, |(start, _)| start);
    if body_length > MAX_NOTE_BYTES {
        return bytes_response(status, common, stream_file(file, start, body_length));
    }
    let body = match read_at(&mut file, start, body_length) {
        Ok(body) => body,
        Err(_) => return json_error(StatusCode::NOT_FOUND, "file not found"),
    };
    bytes_response(status, common, body)
}

async fn handle_put_file(
    State(state): State<Arc<AppState>>,
    Extension(role): Extension<AuthRole>,
    Query(query): Query<FileQuery>,
    body: Body,
) -> Response {
    let requested = query.path.as_deref().unwrap_or_default();
    let relative = match confined_path(&state, requested) {
        Ok(path) if extension_is_markdown(&path) => path,
        _ => {
            return json_error(
                StatusCode::BAD_REQUEST,
                "only vault-relative Markdown files can be updated",
            );
        }
    };
    if !role.is_admin()
        && role.name().is_some_and(|username| {
            !acl_can_access(&state, username, &normalize_snapshot_path(&relative), true)
        })
    {
        return json_error(StatusCode::FORBIDDEN, "access denied");
    }
    let data = match to_bytes(body, MAX_NOTE_BYTES as usize + 1).await {
        Ok(data) if data.len() as u64 <= MAX_NOTE_BYTES => data,
        Ok(_) => {
            return json_error(StatusCode::PAYLOAD_TOO_LARGE, "request body exceeds limit");
        }
        Err(error) => return json_error(StatusCode::PAYLOAD_TOO_LARGE, &error.to_string()),
    };
    let root = match open_current_root(&state) {
        Ok(root) => root,
        Err(error) => {
            return json_error(StatusCode::INTERNAL_SERVER_ERROR, &error);
        }
    };
    if let Some(parent) = relative
        .parent()
        .filter(|parent| !parent.as_os_str().is_empty())
        && let Err(error) = create_parent_directories(&root, parent)
    {
        return json_error(StatusCode::INTERNAL_SERVER_ERROR, &error.to_string());
    }
    if let Err(error) = write_atomic_root(&root, &relative, &data, 0o644) {
        return json_error(StatusCode::INTERNAL_SERVER_ERROR, &error.to_string());
    }
    let file_path = state.vault_root.join(&relative);
    let Some(file_key) = file_path.to_str() else {
        return json_error(
            StatusCode::INTERNAL_SERVER_ERROR,
            "document path is not valid UTF-8",
        );
    };
    if let Ok(document) = parse_bytes(file_key, &data) {
        let indexed = match IndexedDocument::from_vault(&document, None) {
            Ok(indexed) => indexed,
            Err(error) => {
                return json_error(StatusCode::INTERNAL_SERVER_ERROR, &error.to_string());
            }
        };
        let sidecar_path = state
            .vault_root
            .join(".symdesk")
            .join("server")
            .join("sidecar.db");
        let mut sidecar = match Sidecar::open(&sidecar_path) {
            Ok(sidecar) => sidecar,
            Err(error) => {
                return json_error(StatusCode::INTERNAL_SERVER_ERROR, &error.to_string());
            }
        };
        if let Err(error) = sidecar.index_document(&indexed) {
            return json_error(StatusCode::INTERNAL_SERVER_ERROR, &error.to_string());
        }
    }
    json_response(StatusCode::OK, json!({"status": "updated"}))
}

fn extension_is_markdown(path: &Path) -> bool {
    let Some(name) = path.file_name().and_then(|name| name.to_str()) else {
        return false;
    };
    name.rsplit_once('.')
        .is_some_and(|(_, extension)| extension.eq_ignore_ascii_case("md"))
}

fn create_parent_directories(root: &cap_std::fs::Dir, path: &Path) -> io::Result<()> {
    let mut builder = cap_std::fs::DirBuilder::new();
    builder.recursive(true);
    #[cfg(unix)]
    {
        use cap_std::fs::DirBuilderExt;
        builder.mode(0o750);
    }
    root.create_dir_with(path, &builder)
}

fn write_atomic_root(
    root: &cap_std::fs::Dir,
    path: &Path,
    data: &[u8],
    mode: u32,
) -> io::Result<()> {
    #[cfg(not(unix))]
    let _ = mode;
    let parent = path
        .parent()
        .filter(|parent| !parent.as_os_str().is_empty());
    for _ in 0..100 {
        let counter = PUT_TEMP_COUNTER.fetch_add(1, Ordering::Relaxed);
        let name = format!(".symdesk-http-put-{}-{counter}.tmp", std::process::id());
        let temporary = parent.map_or_else(|| PathBuf::from(&name), |parent| parent.join(&name));
        let mut options = cap_std::fs::OpenOptions::new();
        options.write(true).create_new(true);
        #[cfg(unix)]
        {
            use cap_std::fs::OpenOptionsExt;
            options.mode(mode);
        }
        let mut file = match root.open_with(&temporary, &options) {
            Ok(file) => file,
            Err(error) if error.kind() == io::ErrorKind::AlreadyExists => continue,
            Err(error) => return Err(error),
        };
        let result = (|| {
            file.write_all(data)?;
            file.sync_all()?;
            drop(file);
            root.rename(&temporary, root, path)
        })();
        if result.is_err() {
            let _ = root.remove_file(&temporary);
        }
        return result;
    }
    Err(io::Error::new(
        io::ErrorKind::AlreadyExists,
        "create temporary file: too many collisions",
    ))
}

fn range_error_response(path: &Path, message: &str, content_range: Option<String>) -> Response {
    let body = format!("{message}\n").into_bytes();
    let mut headers = vec![
        (
            header::CONTENT_DISPOSITION,
            format!("inline; filename=\"{}\"", safe_filename(path)),
        ),
        (header::CONTENT_TYPE, "text/plain; charset=utf-8".to_owned()),
        (header::CONTENT_LENGTH, body.len().to_string()),
    ];
    if let Some(content_range) = content_range {
        headers.push((header::CONTENT_RANGE, content_range));
    }
    bytes_response(StatusCode::RANGE_NOT_SATISFIABLE, headers, body)
}

fn read_sample(file: &mut cap_std::fs::File, length: u64) -> io::Result<Vec<u8>> {
    file.seek(SeekFrom::Start(0))?;
    let sample_len = length.min(512) as usize;
    let mut sample = vec![0; sample_len];
    file.read_exact(&mut sample)?;
    Ok(sample)
}

fn read_at(file: &mut cap_std::fs::File, start: u64, length: u64) -> io::Result<Vec<u8>> {
    file.seek(SeekFrom::Start(start))?;
    let length = usize::try_from(length)
        .map_err(|_| io::Error::new(io::ErrorKind::InvalidInput, "file is too large"))?;
    let mut body = vec![0; length];
    file.read_exact(&mut body)?;
    Ok(body)
}

fn stream_file(file: cap_std::fs::File, start: u64, length: u64) -> Body {
    let (sender, receiver) = tokio::sync::mpsc::channel(2);
    tokio::task::spawn_blocking(move || {
        let mut file = file;
        if let Err(error) = file.seek(SeekFrom::Start(start)) {
            let _ = sender.blocking_send(Err(error));
            return;
        }
        let mut remaining = length;
        while remaining > 0 {
            let mut chunk = vec![0; remaining.min(64 * 1024) as usize];
            match file.read(&mut chunk) {
                Ok(0) => {
                    let _ =
                        sender.blocking_send(Err(io::Error::from(io::ErrorKind::UnexpectedEof)));
                    return;
                }
                Ok(read) => {
                    remaining -= read as u64;
                    chunk.truncate(read);
                    if sender.blocking_send(Ok(chunk)).is_err() {
                        return;
                    }
                }
                Err(error) => {
                    let _ = sender.blocking_send(Err(error));
                    return;
                }
            }
        }
    });
    Body::from_stream(BodyReceiver(receiver))
}

struct BodyReceiver<T>(tokio::sync::mpsc::Receiver<T>);

impl<T> futures_core::Stream for BodyReceiver<T> {
    type Item = T;

    fn poll_next(self: Pin<&mut Self>, context: &mut Context<'_>) -> Poll<Option<T>> {
        self.get_mut().0.poll_recv(context)
    }
}

async fn handle_not_found() -> Response {
    bytes_response(
        StatusCode::NOT_FOUND,
        vec![(header::CONTENT_TYPE, "text/plain; charset=utf-8".to_owned())],
        b"404 page not found\n".to_vec(),
    )
}

fn open_current_root(state: &AppState) -> Result<cap_std::fs::Dir, String> {
    cap_std::fs::Dir::open_ambient_dir(&state.vault_root, cap_std::ambient_authority())
        .map_err(|error| format!("open vault root: {error}"))
}

fn read_root_file(root: &cap_std::fs::Dir, relative: &Path) -> io::Result<Vec<u8>> {
    let mut options = cap_std::fs::OpenOptions::new();
    options.read(true);
    #[cfg(unix)]
    {
        use cap_std::fs::OpenOptionsExt;
        options.custom_flags(libc::O_NONBLOCK);
    }
    let file = root.open_with(relative, &options)?;
    let metadata = file.metadata()?;
    if !metadata.is_file() {
        return Err(io::Error::new(
            io::ErrorKind::InvalidData,
            "vault path is not a regular file",
        ));
    }
    if metadata.len() > MAX_NOTEBOOK_FILE_BYTES {
        return Err(io::Error::new(
            io::ErrorKind::InvalidData,
            "vault file exceeds 64 MiB read limit",
        ));
    }
    let mut contents = Vec::with_capacity(metadata.len() as usize);
    file.take(MAX_NOTEBOOK_FILE_BYTES + 1)
        .read_to_end(&mut contents)?;
    if contents.len() as u64 > MAX_NOTEBOOK_FILE_BYTES {
        return Err(io::Error::new(
            io::ErrorKind::InvalidData,
            "vault file exceeds 64 MiB read limit",
        ));
    }
    Ok(contents)
}

fn current_root_identity(state: &AppState) -> Option<RootIdentity> {
    #[cfg(unix)]
    {
        let root = open_current_root(state).ok()?;
        let metadata = root.dir_metadata().ok()?;
        use cap_std::fs::MetadataExt;
        Some(RootIdentity::Stable(format!(
            "{}:{}",
            metadata.dev(),
            metadata.ino()
        )))
    }
    #[cfg(windows)]
    {
        Some(RootIdentity::Windows(
            same_file::Handle::from_path(&state.vault_root).ok()?,
        ))
    }
    #[cfg(not(any(unix, windows)))]
    {
        let root = open_current_root(state).ok()?;
        let metadata = root.dir_metadata().ok()?;
        Some(RootIdentity::Stable(format!("{metadata:?}")))
    }
}

#[cfg(windows)]
fn normalize_snapshot_path(path: &Path) -> String {
    path.to_string_lossy().replace('\\', "/")
}

#[cfg(not(windows))]
fn normalize_snapshot_path(path: &Path) -> String {
    path.to_str().unwrap_or_default().to_owned()
}

fn read_snapshot_bytes<R: Read>(reader: R, bytes: &mut Vec<u8>) -> io::Result<usize> {
    reader.take(MAX_NOTE_BYTES + 1).read_to_end(bytes)
}

#[cfg(test)]
#[derive(Default)]
struct InjectedReadFailure {
    emitted: bool,
}

#[cfg(test)]
impl Read for InjectedReadFailure {
    fn read(&mut self, buffer: &mut [u8]) -> io::Result<usize> {
        if self.emitted {
            return Err(io::Error::other("injected snapshot read failure"));
        }
        if buffer.is_empty() {
            return Ok(0);
        }
        self.emitted = true;
        let count = buffer.len().min(3);
        buffer[..count].copy_from_slice(&b"par"[..count]);
        Ok(count)
    }
}

fn snapshot_payload(state: &AppState) -> Result<SnapshotPayload, String> {
    let mut files = Vec::new();
    let mut etag_material = String::new();
    let mut aggregate_note_bytes = 0_u64;
    let root_dir = open_current_root(state)?;
    walk_markdown_with(&state.vault_root, |relative| {
        // Preserve the pre-cache adapter's explicit rejection rather than
        // silently aliasing invalid paths to empty or replacement strings.
        if relative.to_str().is_none() {
            return Err(io::Error::other("vault path is not UTF-8"));
        }
        let logical_path = normalize_snapshot_path(relative);
        let mut file = match root_dir.open(relative) {
            Ok(file) => file,
            // An external symlink, a concurrently removed file, and a file
            // replaced by an escaping symlink are all intentionally omitted.
            // The opened capability is the security boundary; no path is read
            // again after this point.
            Err(_) => return Ok(()),
        };
        let metadata = match file.metadata() {
            Ok(metadata) if metadata.is_file() && metadata.len() <= MAX_NOTE_BYTES => metadata,
            _ => return Ok(()),
        };
        // Reserve for JSON keys, timestamps, path/ETag material, and separators
        // before allocating the note content. The serialized snapshot is
        // checked again below for exact plain and gzip bounds.
        let note_overhead = SNAPSHOT_NOTE_OVERHEAD_BYTES
            .checked_add(logical_path.len() as u64)
            .ok_or_else(|| io::Error::other(SNAPSHOT_TOO_LARGE))?;
        aggregate_note_bytes = aggregate_note_bytes
            .checked_add(metadata.len())
            .and_then(|size| size.checked_add(note_overhead))
            .ok_or_else(|| io::Error::other(SNAPSHOT_TOO_LARGE))?;
        if aggregate_note_bytes > MAX_SNAPSHOT_BYTES {
            return Err(io::Error::other(SNAPSHOT_TOO_LARGE));
        }
        let mut bytes =
            Vec::with_capacity((metadata.len() as usize).min(MAX_NOTE_BYTES as usize + 1));
        #[cfg(test)]
        let injected_failure = state.snapshot_cache.take_read_failure();
        #[cfg(test)]
        let read_result = if injected_failure {
            read_snapshot_bytes(InjectedReadFailure::default(), &mut bytes)
        } else {
            read_snapshot_bytes(&mut file, &mut bytes)
        };
        #[cfg(not(test))]
        let read_result = read_snapshot_bytes(&mut file, &mut bytes);
        read_result?;
        if bytes.len() as u64 > MAX_NOTE_BYTES {
            return Ok(());
        }
        let modified = metadata
            .modified()
            .map(|value| value.into_std())
            .unwrap_or(UNIX_EPOCH);
        let mtime_ns = unix_nanos(modified);
        let _ = writeln!(etag_material, "{logical_path}\0{}\0{mtime_ns}", bytes.len());
        files.push(SnapshotNote {
            path: logical_path,
            content: String::from_utf8_lossy(&bytes).into_owned(),
            modified_at: format_rfc3339(modified),
        });
        Ok(())
    })
    .map_err(|error| error.to_string())?;
    let etag = symdesk_vault::sha256_hex(etag_material.as_bytes());
    let snapshot = Snapshot {
        generated_at: format_rfc3339(SystemTime::now()),
        notes: files,
    };
    let mut plain = serde_json::to_vec(&snapshot).map_err(|error| error.to_string())?;
    plain.push(b'\n');
    if plain.len() as u64 > MAX_SNAPSHOT_BYTES {
        return Err(SNAPSHOT_TOO_LARGE.to_owned());
    }
    let mut encoder = GzEncoder::new(Vec::new(), Compression::default());
    std::io::Write::write_all(&mut encoder, &plain).map_err(|error| error.to_string())?;
    let compressed = encoder.finish().map_err(|error| error.to_string())?;
    if compressed.len() as u64 > MAX_SNAPSHOT_BYTES {
        return Err(SNAPSHOT_TOO_LARGE.to_owned());
    }
    Ok(SnapshotPayload {
        plain: plain.into(),
        compressed: compressed.into(),
        etag,
    })
}

fn confined_path(_state: &AppState, requested: &str) -> Result<PathBuf, PathError> {
    if requested.is_empty()
        || requested.contains('\0')
        || requested.contains('\\')
        || Path::new(requested).is_absolute()
        || (requested != "."
            && requested
                .split('/')
                .any(|component| component.is_empty() || component == "." || component == ".."))
    {
        return Err(PathError::Invalid);
    }
    let relative = Path::new(requested);
    if relative.components().any(|component| {
        matches!(
            component,
            Component::ParentDir | Component::RootDir | Component::Prefix(_)
        )
    }) {
        return Err(PathError::Invalid);
    }
    if relative
        .components()
        .next()
        .is_some_and(|component| component.as_os_str() == ".symdesk")
    {
        return Err(PathError::Invalid);
    }
    Ok(relative.to_path_buf())
}

fn parse_range(value: &str, length: u64) -> Result<(u64, u64), RangeError> {
    let value = value.strip_prefix("bytes=").ok_or(RangeError::Invalid)?;
    if value.contains(',') {
        return Err(RangeError::Invalid);
    }
    let (start, end) = value.split_once('-').ok_or(RangeError::Invalid)?;
    if start.is_empty() {
        let suffix = end.parse::<u64>().map_err(|_| RangeError::Invalid)?;
        if suffix == 0 || length == 0 {
            return Err(RangeError::Invalid);
        }
        return Ok((length.saturating_sub(suffix), length - 1));
    }
    let start = start.parse::<u64>().map_err(|_| RangeError::Invalid)?;
    if start >= length {
        return Err(RangeError::NoOverlap);
    }
    let end = if end.is_empty() {
        length - 1
    } else {
        end.parse::<u64>()
            .map_err(|_| RangeError::Invalid)?
            .min(length - 1)
    };
    if start > end {
        return Err(RangeError::Invalid);
    }
    Ok((start, end))
}

fn content_type(path: &Path, sample: &[u8]) -> String {
    #[cfg(any(unix, target_os = "windows"))]
    {
        content_type_with_mime_loader(path, sample, mime::type_by_extension)
    }
    #[cfg(not(any(unix, target_os = "windows")))]
    {
        content_type_with_mime_loader(path, sample, |_| None)
    }
}

fn content_type_with_mime_loader(
    path: &Path,
    sample: &[u8],
    mime_loader: impl Fn(&str) -> Option<String>,
) -> String {
    let extension = path
        .extension()
        .and_then(|ext| ext.to_str())
        .unwrap_or_default();
    if let Some(system_type) =
        mime_loader(&format!(".{extension}")).filter(|system_type| !system_type.is_empty())
    {
        return system_type;
    }
    match extension.to_ascii_lowercase().as_str() {
        "md" => "text/plain; charset=utf-8".to_owned(),
        "txt" => "text/plain; charset=utf-8".to_owned(),
        "json" => "application/json".to_owned(),
        "html" | "htm" => "text/html; charset=utf-8".to_owned(),
        "css" => "text/css; charset=utf-8".to_owned(),
        "js" => "text/javascript; charset=utf-8".to_owned(),
        "xml" => "application/xml".to_owned(),
        "pdf" => "application/pdf".to_owned(),
        "png" => "image/png".to_owned(),
        "jpg" | "jpeg" => "image/jpeg".to_owned(),
        "gif" => "image/gif".to_owned(),
        "svg" => "image/svg+xml".to_owned(),
        _ => detect_content_type(sample),
    }
}

fn detect_content_type(sample: &[u8]) -> String {
    if sample.starts_with(&[0x89, b'P', b'N', b'G', 0x0d, 0x0a, 0x1a, 0x0a]) {
        return "image/png".to_owned();
    }
    if sample.starts_with(&[0xff, 0xd8, 0xff]) {
        return "image/jpeg".to_owned();
    }
    if sample.starts_with(b"GIF87a") || sample.starts_with(b"GIF89a") {
        return "image/gif".to_owned();
    }
    if sample.starts_with(b"%PDF-") {
        return "application/pdf".to_owned();
    }
    if sample.iter().all(|byte| {
        *byte == b'\t' || *byte == b'\n' || *byte == b'\r' || (0x20..=0x7e).contains(byte)
    }) {
        return "text/plain; charset=utf-8".to_owned();
    }
    "application/octet-stream".to_owned()
}

fn safe_filename(path: &Path) -> String {
    let value = path
        .file_name()
        .and_then(|name| name.to_str())
        .unwrap_or_default();
    safe_filename_value(value)
}

fn safe_filename_value(value: &str) -> String {
    let value = value.replace('\\', "/");
    let value = value.rsplit('/').next().unwrap_or_default().trim();
    if value.is_empty() || value == "." {
        "document.bin".to_owned()
    } else {
        value.to_owned()
    }
}

fn format_rfc3339(value: SystemTime) -> String {
    let nanos = unix_nanos(value);
    OffsetDateTime::from_unix_timestamp_nanos(i128::from(nanos))
        .ok()
        .and_then(|value| value.format(&Rfc3339).ok())
        .unwrap_or_else(|| "1970-01-01T00:00:00Z".to_owned())
}

fn unix_nanos(value: SystemTime) -> i64 {
    match value.duration_since(UNIX_EPOCH) {
        Ok(duration) => i64::try_from(duration.as_nanos()).unwrap_or(i64::MAX),
        Err(error) => -i64::try_from(error.duration().as_nanos()).unwrap_or(i64::MAX),
    }
}

fn constant_time_equal(left: &[u8], right: &[u8]) -> bool {
    let mut difference = left.len() ^ right.len();
    for index in 0..left.len().max(right.len()) {
        difference |= usize::from(left.get(index).copied().unwrap_or_default())
            ^ usize::from(right.get(index).copied().unwrap_or_default());
    }
    difference == 0
}

fn json_response(status: StatusCode, value: impl Serialize) -> Response {
    let mut body = serde_json::to_vec(&value)
        .unwrap_or_else(|_| b"{\"error\":\"serialization failed\"}".to_vec());
    body.push(b'\n');
    bytes_response(
        status,
        vec![
            (header::CONTENT_TYPE, "application/json".to_owned()),
            (header::CONTENT_LENGTH, body.len().to_string()),
        ],
        body,
    )
}

fn json_error(status: StatusCode, message: &str) -> Response {
    json_response(status, json!({"error": message}))
}

fn bytes_response(
    status: StatusCode,
    headers: Vec<(header::HeaderName, String)>,
    body: impl Into<Body>,
) -> Response {
    let mut response = Response::new(body.into());
    *response.status_mut() = status;
    for (name, value) in headers {
        if let Ok(value) = HeaderValue::try_from(value) {
            response.headers_mut().insert(name, value);
        }
    }
    response
}

async fn shutdown_signal() {
    #[cfg(unix)]
    {
        let mut terminate =
            match tokio::signal::unix::signal(tokio::signal::unix::SignalKind::terminate()) {
                Ok(signal) => signal,
                Err(_) => {
                    let _ = tokio::signal::ctrl_c().await;
                    return;
                }
            };
        tokio::select! {
            _ = tokio::signal::ctrl_c() => {},
            _ = terminate.recv() => {},
        }
    }
    #[cfg(not(unix))]
    {
        let _ = tokio::signal::ctrl_c().await;
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[cfg(unix)]
    #[tokio::test]
    async fn command_stream_is_line_buffered_and_emits_terminal_failure_event() {
        let mut child = TokioCommand::new("sh")
            .args([
                "-c",
                "printf 'first\\npartial'; printf 'failure' >&2; exit 7",
            ])
            .stdout(Stdio::piped())
            .stderr(Stdio::piped())
            .kill_on_drop(true)
            .spawn()
            .unwrap();
        let stdout = child.stdout.take().unwrap();
        let stderr = child.stderr.take().unwrap();
        let stderr_task = tokio::spawn(read_command_pipe(stderr, MAX_COMMAND_STDERR_BYTES));
        let (sender, mut receiver) = mpsc::channel(1);
        let relay = relay_command_ndjson(stdout, &sender, &mut child, 128);
        tokio::pin!(relay);
        let mut events = Vec::new();
        let status = loop {
            tokio::select! {
                result = &mut relay => break result,
                event = receiver.recv() => events.push(event.unwrap().unwrap()),
            }
        };
        while let Ok(event) = receiver.try_recv() {
            events.push(event.unwrap());
        }

        assert_eq!(events[0], Bytes::from_static(b"first\n"));
        assert_eq!(events[1], Bytes::from_static(b"partial"));
        let status = status.unwrap().unwrap();
        assert_eq!(status.code(), Some(7));
        send_command_error(&sender, "failure").await.unwrap();
        let event: serde_json::Value =
            serde_json::from_slice(&receiver.recv().await.unwrap().unwrap()).unwrap();
        assert_eq!(event["type"], "error");
        assert_eq!(event["message"], "failure");
        let stderr = stderr_task.await.unwrap().unwrap().0;
        assert_eq!(stderr, b"failure");
    }

    #[cfg(unix)]
    #[tokio::test]
    async fn command_stream_bounds_lines_and_kills_child_when_client_disconnects() {
        let mut child = TokioCommand::new("sh")
            .args(["-c", "printf '123456\\n'; exec sleep 30"])
            .stdout(Stdio::piped())
            .kill_on_drop(true)
            .spawn()
            .unwrap();
        let stdout = child.stdout.take().unwrap();
        let (sender, mut receiver) = mpsc::channel(1);
        let relay = relay_command_ndjson(stdout, &sender, &mut child, 5);
        let (result, event) = tokio::join!(relay, receiver.recv());

        assert!(result.unwrap().is_none());
        let event: serde_json::Value = serde_json::from_slice(&event.unwrap().unwrap()).unwrap();
        assert_eq!(event["type"], "error");
        assert_eq!(event["message"], "command output exceeded 32 MiB");

        let mut child = TokioCommand::new("sh")
            .args(["-c", "printf 'ready\\n'; exec sleep 30"])
            .stdout(Stdio::piped())
            .kill_on_drop(true)
            .spawn()
            .unwrap();
        let stdout = child.stdout.take().unwrap();
        let (sender, mut receiver) = mpsc::channel(1);
        let relay = relay_command_ndjson(stdout, &sender, &mut child, MAX_COMMAND_OUTPUT_BYTES);
        tokio::pin!(relay);
        let first = tokio::select! {
            event = receiver.recv() => event.unwrap().unwrap(),
            result = &mut relay => panic!("relay ended before emitting first line: {result:?}"),
        };
        assert_eq!(first, Bytes::from_static(b"ready\n"));
        drop(receiver);
        assert!(
            tokio::time::timeout(Duration::from_secs(2), &mut relay)
                .await
                .unwrap()
                .unwrap()
                .is_none()
        );
    }

    #[tokio::test]
    async fn command_capture_truncates_without_growing_past_the_limit() {
        let input = vec![b'x'; 64 << 10];
        let (captured, overflow) = read_command_pipe(&input[..], 8).await.unwrap();
        assert!(overflow);
        assert_eq!(captured, b"xxxxxxxx");
    }

    #[test]
    fn command_environment_scrubs_auth_tokens_and_sets_sidecar() {
        let root = Path::new("/vault/root");
        let env = filtered_subprocess_env(
            [
                (OsString::from("PATH"), OsString::from("/bin")),
                (
                    OsString::from("SYMDESK_SERVER_TOKEN"),
                    OsString::from("server-secret"),
                ),
                (
                    OsString::from("SYMDESK_WORKER_TOKEN"),
                    OsString::from("worker-secret"),
                ),
            ],
            root,
        );
        assert!(
            env.iter()
                .any(|(key, value)| key == "PATH" && value == "/bin")
        );
        assert!(
            !env.iter()
                .any(|(key, _)| { key == "SYMDESK_SERVER_TOKEN" || key == "SYMDESK_WORKER_TOKEN" })
        );
        assert!(env.iter().any(|(key, value)| {
            key == "SYMDESK_SIDECAR" && value == "/vault/root/.symdesk/server/sidecar.db"
        }));
    }

    #[test]
    fn worker_acl_defaults_public_but_fails_closed_for_malformed_policy_or_root() {
        let root = std::env::temp_dir().join(format!(
            "symdesk-worker-acl-{}-{}",
            std::process::id(),
            unix_nanos(SystemTime::now())
        ));
        fs::create_dir_all(&root).unwrap();
        let state = test_state(&root, "test token");
        assert!(acl_can_access(&state, "worker", "note.md", false));
        assert!(acl_can_access(&state, "worker", "note.md", true));
        assert!(acl_can_read_many(&state, "worker", &["note.md".to_owned()]).contains("note.md"));

        fs::create_dir_all(root.join(".symdesk")).unwrap();
        fs::write(root.join(".symdesk/permissions.json"), b"[{").unwrap();
        assert!(!acl_can_access(&state, "worker", "note.md", false));
        assert!(!acl_can_access(&state, "worker", "note.md", true));
        assert!(acl_can_read_many(&state, "worker", &["note.md".to_owned()]).is_empty());

        let missing_root = root.join("missing");
        let unavailable = test_state(&missing_root, "test token");
        assert!(!acl_can_access(&unavailable, "worker", "note.md", false));
        assert!(!acl_can_access(&unavailable, "worker", "note.md", true));
        assert!(acl_can_read_many(&unavailable, "worker", &["note.md".to_owned()]).is_empty());
        fs::remove_dir_all(root).unwrap();
    }

    #[test]
    fn named_user_authentication_matches_stored_sha256_token_hash() {
        let root = std::env::temp_dir().join(format!(
            "symdesk-named-auth-{}-{}",
            std::process::id(),
            unix_nanos(SystemTime::now())
        ));
        fs::create_dir_all(root.join(".symdesk")).unwrap();
        let token = "named fixture token";
        let hash = symdesk_vault::sha256_hex(token.as_bytes());
        fs::write(
            root.join(".symdesk/users.json"),
            format!(r#"[{{"name":"alice","token_hash":"{hash}","roles":["user"]}}]"#),
        )
        .unwrap();
        let state = test_state(&root, "admin fixture token");

        assert_eq!(
            authenticate_named_user(&state, token),
            Some(AuthRole::User {
                name: "alice".to_owned(),
                roles: vec!["user".to_owned()],
            })
        );
        assert_eq!(authenticate_named_user(&state, "wrong token"), None);
        assert_eq!(authenticate_named_user(&state, ""), None);
        fs::remove_dir_all(root).unwrap();
    }

    #[test]
    fn named_worker_role_is_limited_to_worker_routes() {
        let worker = AuthRole::User {
            name: "worker-user".to_owned(),
            roles: vec!["worker".to_owned()],
        };
        for (method, path) in [
            (Method::POST, "/api/v1/worker/lease"),
            (Method::GET, "/api/v1/worker/input"),
            (Method::POST, "/api/v1/worker/complete"),
            (Method::POST, "/api/v1/worker/fail"),
        ] {
            assert!(
                named_user_route_allowed(&worker, &method, path),
                "{method} {path}"
            );
        }
        assert!(!named_user_route_allowed(
            &worker,
            &Method::GET,
            "/api/v1/jobs"
        ));
        assert!(route_requires_admin(&Method::GET, "/api/v1/jobs"));
        assert!(!named_user_route_allowed(
            &worker,
            &Method::POST,
            "/api/v1/command"
        ));

        let user = AuthRole::User {
            name: "alice".to_owned(),
            roles: vec!["user".to_owned()],
        };
        assert!(!named_user_route_allowed(
            &user,
            &Method::POST,
            "/api/v1/worker/lease"
        ));
    }

    #[test]
    fn completion_yaml_scalars_match_go_yaml_v3_style_choices() {
        for (value, expected) in [
            ("yes", "\"yes\""),
            ("on", "\"on\""),
            ("2026-01-02", "\"2026-01-02\""),
            ("123", "\"123\""),
            ("a: b", "'a: b'"),
            ("x #y", "'x #y'"),
            (" leading", "' leading'"),
            ("é", "é"),
        ] {
            assert_eq!(yaml_scalar(value), expected, "scalar {value:?}");
        }
    }

    #[test]
    fn worker_token_validation_matches_go_configuration_contract() {
        let admin = "0123456789abcdef0123456789abcdef";
        let worker = "fedcba9876543210fedcba9876543210";
        assert_eq!(validate_tokens(admin, None), Ok(()));
        assert_eq!(
            validate_tokens(admin, Some("short")),
            Err("worker token must contain at least 32 characters".to_owned())
        );
        assert_eq!(
            validate_tokens(admin, Some(admin)),
            Err("worker token must differ from the server token".to_owned())
        );
        assert_eq!(validate_tokens(admin, Some(worker)), Ok(()));
    }

    #[test]
    fn worker_auth_route_policy_matches_admin_boundaries() {
        for (method, path, expected) in [
            (Method::GET, "/api/v1/jobs", true),
            (Method::HEAD, "/api/v1/jobs", true),
            (Method::POST, "/api/v1/jobs/retry", true),
            (Method::POST, "/api/v1/ingest", true),
            (Method::POST, "/api/v1/command", true),
            (Method::GET, "/api/v1/status", false),
            (Method::PUT, "/api/v1/files", false),
            (Method::POST, "/api/v1/worker/lease", false),
        ] {
            assert_eq!(
                route_requires_admin(&method, path),
                expected,
                "{method} {path}"
            );
        }
    }

    #[tokio::test]
    async fn create_share_matches_admin_contract_and_persists_only_token_hash() {
        let root = std::env::temp_dir().join(format!(
            "symdesk-protocol-create-share-{}-{}",
            std::process::id(),
            unix_nanos(SystemTime::now())
        ));
        fs::create_dir_all(root.join("notes")).unwrap();
        fs::write(root.join("notes/readme.md"), "shareable").unwrap();
        let app = router(Arc::new(test_state(
            &root,
            "a sufficiently long test token",
        )));
        let request = |body: &[u8], authenticated: bool| {
            let mut builder = Request::builder().method(Method::POST).uri("/api/v1/share");
            if authenticated {
                builder = builder.header(
                    header::AUTHORIZATION,
                    "Bearer a sufficiently long test token",
                );
            }
            builder.body(Body::from(body.to_vec())).unwrap()
        };

        assert_eq!(
            app.clone()
                .oneshot(request(br#"{"path":"notes/readme.md","expiry":1}"#, false))
                .await
                .unwrap()
                .status(),
            StatusCode::UNAUTHORIZED
        );
        for (body, status) in [
            (b"{".as_slice(), StatusCode::BAD_REQUEST),
            (br#"{"expiry":1}"#.as_slice(), StatusCode::BAD_REQUEST),
            (
                br#"{"path":"../secret.md","expiry":1}"#.as_slice(),
                StatusCode::BAD_REQUEST,
            ),
            (
                br#"{"path":".symdesk/server/shares.json","expiry":1}"#.as_slice(),
                StatusCode::BAD_REQUEST,
            ),
            (
                br#"{"path":"datasets/private.md","expiry":0}"#.as_slice(),
                StatusCode::FORBIDDEN,
            ),
            (
                br#"{"path":"notes/readme.md","expiry":0}"#.as_slice(),
                StatusCode::BAD_REQUEST,
            ),
            (
                br#"{"path":"notes/missing.md","expiry":1}"#.as_slice(),
                StatusCode::NOT_FOUND,
            ),
        ] {
            let response = app.clone().oneshot(request(body, true)).await.unwrap();
            assert_eq!(
                response.status(),
                status,
                "body: {}",
                String::from_utf8_lossy(body)
            );
        }

        let response = app
            .oneshot(request(br#"{"path":"notes/readme.md","expiry":1}"#, true))
            .await
            .unwrap();
        assert_eq!(response.status(), StatusCode::CREATED);
        let response: serde_json::Value =
            serde_json::from_slice(&to_bytes(response.into_body(), 1 << 20).await.unwrap())
                .unwrap();
        let id = response["id"].as_str().unwrap();
        let token = response["token"].as_str().unwrap();
        assert_eq!(id.len(), 24);
        assert_eq!(token.len(), 64);
        assert_eq!(response["path"], "notes/readme.md");
        assert_eq!(response["url"], format!("/s/{token}"));

        let stored = fs::read(root.join(".symdesk/server/shares.json")).unwrap();
        let shares: serde_json::Value = serde_json::from_slice(&stored).unwrap();
        assert_eq!(shares[0]["id"], id);
        assert_eq!(shares[0]["created_by"], "admin");
        assert_eq!(
            shares[0]["token_hash"],
            symdesk_vault::sha256_hex(token.as_bytes())
        );
        assert!(String::from_utf8(stored).unwrap().find(token).is_none());
        #[cfg(unix)]
        assert_eq!(
            std::os::unix::fs::PermissionsExt::mode(
                &fs::metadata(root.join(".symdesk/server/shares.json"))
                    .unwrap()
                    .permissions(),
            ) & 0o777,
            0o600
        );
        fs::remove_dir_all(root).unwrap();
    }

    #[tokio::test]
    async fn get_shares_requires_admin_token_and_omits_secrets() {
        let root = std::env::temp_dir().join(format!(
            "symdesk-protocol-shares-{}-{}",
            std::process::id(),
            unix_nanos(SystemTime::now())
        ));
        let server_dir = root.join(".symdesk/server");
        fs::create_dir_all(&server_dir).unwrap();
        fs::write(
            server_dir.join("shares.json"),
            br#"[
                {"id":"old","path":"notes/old.md","created_by":"admin","created_at":"2026-01-01T00:00:00Z","expires_at":"2026-01-02T00:00:00Z","token_hash":"old-hash"},
                {"id":"new","path":"notes/new.md","created_by":"admin","created_at":"2026-01-03T00:00:00Z","expires_at":"2026-01-04T00:00:00Z","token_hash":"secret-hash","token":"secret-token","expired":true,"revoked_at":"2026-01-03T12:00:00Z"}
            ]"#,
        )
        .unwrap();
        let app = router(Arc::new(test_state(
            &root,
            "a sufficiently long test token",
        )));
        let denied = app
            .clone()
            .oneshot(
                Request::builder()
                    .uri("/api/v1/shares")
                    .body(Body::empty())
                    .unwrap(),
            )
            .await
            .unwrap();
        assert_eq!(denied.status(), StatusCode::UNAUTHORIZED);

        let response = app
            .clone()
            .oneshot(
                Request::builder()
                    .uri("/api/v1/shares")
                    .header(
                        header::AUTHORIZATION,
                        "Bearer a sufficiently long test token",
                    )
                    .body(Body::empty())
                    .unwrap(),
            )
            .await
            .unwrap();
        assert_eq!(response.status(), StatusCode::OK);
        let body = to_bytes(response.into_body(), 1 << 20).await.unwrap();
        let shares: serde_json::Value = serde_json::from_slice(&body).unwrap();
        assert_eq!(shares[0]["id"], "new");
        assert_eq!(shares[0]["expired"], true);
        assert_eq!(shares[1]["id"], "old");
        assert_eq!(shares[0]["token_hash"], "");
        assert!(shares[0].get("token").is_none());
        assert!(shares[1].get("expired").is_none());

        fs::write(
            server_dir.join("shares.json"),
            vec![b' '; MAX_SHARE_STORE_BYTES as usize + 1],
        )
        .unwrap();
        let too_large = app
            .oneshot(
                Request::builder()
                    .uri("/api/v1/shares")
                    .header(
                        header::AUTHORIZATION,
                        "Bearer a sufficiently long test token",
                    )
                    .body(Body::empty())
                    .unwrap(),
            )
            .await
            .unwrap();
        assert_eq!(too_large.status(), StatusCode::INTERNAL_SERVER_ERROR);
        assert_eq!(
            to_bytes(too_large.into_body(), 1 << 20)
                .await
                .unwrap()
                .as_ref(),
            &br#"{"error":"failed to list shares"}
"#[..]
        );
        fs::remove_dir_all(root).unwrap();
    }

    #[tokio::test]
    async fn public_share_streams_files_over_the_buffer_limit() {
        let root = std::env::temp_dir().join(format!(
            "symdesk-protocol-share-large-{}-{}",
            std::process::id(),
            unix_nanos(SystemTime::now())
        ));
        fs::create_dir_all(root.join(".symdesk/server")).unwrap();
        let file = fs::File::create(root.join("large.bin")).unwrap();
        file.set_len(MAX_NOTE_BYTES + 1).unwrap();
        fs::write(
            root.join(".symdesk/server/shares.json"),
            serde_json::to_vec(&[ShareLink {
                id: "large".to_owned(),
                path: "large.bin".to_owned(),
                created_by: "admin".to_owned(),
                created_at: "2026-01-01T00:00:00Z".to_owned(),
                expires_at: "2099-01-01T00:00:00Z".to_owned(),
                token_hash: symdesk_vault::sha256_hex(b"large-share-token"),
                expired: false,
                revoked_at: None,
            }])
            .unwrap(),
        )
        .unwrap();

        let response = router(Arc::new(test_state(
            &root,
            "a sufficiently long test token",
        )))
        .oneshot(
            Request::builder()
                .uri("/s/large-share-token")
                .body(Body::empty())
                .unwrap(),
        )
        .await
        .unwrap();
        assert_eq!(response.status(), StatusCode::OK);
        let body = to_bytes(response.into_body(), (MAX_NOTE_BYTES + 1) as usize)
            .await
            .unwrap();
        assert_eq!(body.len(), (MAX_NOTE_BYTES + 1) as usize);
        assert!(body.iter().all(|byte| *byte == 0));
        fs::remove_dir_all(root).unwrap();
    }

    #[tokio::test]
    async fn ingest_requires_auth_and_persists_confined_upload_and_private_job() {
        let root = std::env::temp_dir().join(format!(
            "symdesk-protocol-ingest-{}-{}",
            std::process::id(),
            unix_nanos(SystemTime::now())
        ));
        fs::create_dir(&root).unwrap();
        let app = router(Arc::new(test_state(
            &root,
            "a sufficiently long test token",
        )));
        let boundary = "symdesk-ingest-test-boundary";
        let body = format!(
            "--{boundary}\r\nContent-Disposition: form-data; name=\"file\"; filename=\"../invoice.pdf\"\r\nContent-Type: application/pdf\r\n\r\npdf bytes\r\n--{boundary}--\r\n"
        );
        let request = |authorized: bool| {
            let mut builder = Request::builder()
                .method(Method::POST)
                .uri("/api/v1/ingest")
                .header(
                    header::CONTENT_TYPE,
                    format!("multipart/form-data; boundary={boundary}"),
                );
            if authorized {
                builder = builder.header(
                    header::AUTHORIZATION,
                    "Bearer a sufficiently long test token",
                );
            }
            builder.body(Body::from(body.clone())).unwrap()
        };
        let denied = app.clone().oneshot(request(false)).await.unwrap();
        assert_eq!(denied.status(), StatusCode::UNAUTHORIZED);
        assert!(!root.join("archive").exists());

        let malformed = app
            .clone()
            .oneshot(
                Request::builder()
                    .method(Method::POST)
                    .uri("/api/v1/ingest")
                    .header(
                        header::AUTHORIZATION,
                        "Bearer a sufficiently long test token",
                    )
                    .header(
                        header::CONTENT_TYPE,
                        format!("multipart/form-data; boundary={boundary}"),
                    )
                    .body(Body::from(format!(
                        "--{boundary}\r\nContent-Disposition: form-data; name=\"file\"; filename=\"bad.pdf\"\r\n\r\nbroken"
                    )))
                    .unwrap(),
            )
            .await
            .unwrap();
        assert_eq!(malformed.status(), StatusCode::PAYLOAD_TOO_LARGE);
        assert_eq!(
            to_bytes(malformed.into_body(), 1 << 20)
                .await
                .unwrap()
                .as_ref(),
            &br#"{"error":"upload exceeds 100 MiB or is invalid"}
"#[..]
        );

        let missing_file_boundary = "symdesk-ingest-missing-file";
        let missing_file = app
            .clone()
            .oneshot(
                Request::builder()
                    .method(Method::POST)
                    .uri("/api/v1/ingest")
                    .header(
                        header::AUTHORIZATION,
                        "Bearer a sufficiently long test token",
                    )
                    .header(
                        header::CONTENT_TYPE,
                        format!("multipart/form-data; boundary={missing_file_boundary}"),
                    )
                    .body(Body::from(format!(
                        "--{missing_file_boundary}\r\nContent-Disposition: form-data; name=\"other\"\r\n\r\nvalue\r\n--{missing_file_boundary}--\r\n"
                    )))
                    .unwrap(),
            )
            .await
            .unwrap();
        assert_eq!(missing_file.status(), StatusCode::BAD_REQUEST);

        let response = app.oneshot(request(true)).await.unwrap();
        assert_eq!(response.status(), StatusCode::ACCEPTED);
        let job: serde_json::Value =
            serde_json::from_slice(&to_bytes(response.into_body(), 1 << 20).await.unwrap())
                .unwrap();
        assert_eq!(job["status"], "pending");
        assert_eq!(job["original_name"], "invoice.pdf");
        assert_eq!(job["content_type"], "application/pdf");
        let source = job["source_path"].as_str().unwrap();
        assert!(source.starts_with("archive/"));
        assert!(source.ends_with("-invoice.pdf"));
        let archive_id = Path::new(source)
            .file_name()
            .unwrap()
            .to_str()
            .unwrap()
            .split_once('-')
            .unwrap()
            .0;
        assert_ne!(archive_id, job["id"].as_str().unwrap());
        assert_eq!(fs::read(root.join(source)).unwrap(), b"pdf bytes");
        let job_path = root
            .join(".symdesk/server/jobs")
            .join(format!("{}.json", job["id"].as_str().unwrap()));
        assert_eq!(
            serde_json::from_slice::<JobRecord>(&fs::read(&job_path).unwrap())
                .unwrap()
                .status,
            "pending"
        );
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            assert_eq!(
                fs::metadata(root.join(source))
                    .unwrap()
                    .permissions()
                    .mode()
                    & 0o777,
                0o640
            );
            assert_eq!(
                fs::metadata(job_path).unwrap().permissions().mode() & 0o777,
                0o600
            );
        }
        fs::remove_dir_all(&root).unwrap();
    }

    #[tokio::test]
    async fn get_jobs_requires_auth_and_returns_newest_first_pages() {
        let root = std::env::temp_dir().join(format!(
            "symdesk-protocol-jobs-{}-{}",
            std::process::id(),
            unix_nanos(SystemTime::now())
        ));
        let jobs = root.join(".symdesk/server/jobs");
        fs::create_dir_all(&jobs).unwrap();
        fs::write(
            jobs.join("00000000000000000000000000000001.json"),
            r#"{"id":"00000000000000000000000000000001","schema_version":1,"status":"pending","source_path":"inbox/one.pdf","original_name":"one.pdf","capability":"ocr","created_at":"2026-01-02T03:04:05Z","updated_at":"2026-01-02T03:04:05Z"}"#,
        )
        .unwrap();
        fs::write(
            jobs.join("00000000000000000000000000000002.json"),
            r#"{"id":"00000000000000000000000000000002","schema_version":1,"status":"completed","source_path":"inbox/two.pdf","original_name":"two.pdf","capability":"ocr","created_at":"2026-01-03T03:04:05Z","updated_at":"2026-01-03T03:04:05Z"}"#,
        )
        .unwrap();
        for index in 3..=101 {
            let id = format!("{index:032x}");
            fs::write(
                jobs.join(format!("{id}.json")),
                format!(
                    "{{\"id\":\"{id}\",\"schema_version\":1,\"status\":\"pending\",\"source_path\":\"old.pdf\",\"original_name\":\"old.pdf\",\"capability\":\"ocr\",\"created_at\":\"2025-01-01T00:00:00Z\",\"updated_at\":\"2025-01-01T00:00:00Z\"}}"
                ),
            )
            .unwrap();
        }
        let app = router(Arc::new(test_state(
            &root,
            "a sufficiently long test token",
        )));
        let unauthorized = app
            .clone()
            .oneshot(
                Request::builder()
                    .uri("/api/v1/jobs")
                    .body(Body::empty())
                    .unwrap(),
            )
            .await
            .unwrap();
        assert_eq!(unauthorized.status(), StatusCode::UNAUTHORIZED);
        let unpaged = app
            .clone()
            .oneshot(
                Request::builder()
                    .uri("/api/v1/jobs")
                    .header(
                        header::AUTHORIZATION,
                        "Bearer a sufficiently long test token",
                    )
                    .body(Body::empty())
                    .unwrap(),
            )
            .await
            .unwrap();
        assert_eq!(unpaged.status(), StatusCode::OK);
        let unpaged_body = to_bytes(unpaged.into_body(), 1 << 20).await.unwrap();
        let unpaged_jobs: serde_json::Value = serde_json::from_slice(&unpaged_body).unwrap();
        assert_eq!(unpaged_jobs.as_array().unwrap().len(), 100);
        assert_eq!(unpaged_jobs[0]["status"], "completed");
        let response = app
            .oneshot(
                Request::builder()
                    .uri("/api/v1/jobs?limit=1&offset=1")
                    .header(
                        header::AUTHORIZATION,
                        "Bearer a sufficiently long test token",
                    )
                    .body(Body::empty())
                    .unwrap(),
            )
            .await
            .unwrap();
        assert_eq!(response.status(), StatusCode::OK);
        assert_eq!(response.headers()[header::CONTENT_TYPE], "application/json");
        let expected = concat!(
            "{\"jobs\":[{\"id\":\"00000000000000000000000000000001\",\"schema_version\":1,",
            "\"status\":\"pending\",\"source_path\":\"inbox/one.pdf\",",
            "\"original_name\":\"one.pdf\",\"capability\":\"ocr\",",
            "\"created_at\":\"2026-01-02T03:04:05Z\",",
            "\"updated_at\":\"2026-01-02T03:04:05Z\"}],\"total\":101,\"limit\":1,\"offset\":1}\n"
        );
        assert_eq!(
            to_bytes(response.into_body(), 1 << 20).await.unwrap(),
            expected.as_bytes()
        );
        fs::remove_dir_all(root).unwrap();
    }

    #[cfg(unix)]
    #[tokio::test]
    async fn retry_job_resets_failed_state_and_persists_private_file() {
        use std::os::unix::fs::PermissionsExt;

        let root = std::env::temp_dir().join(format!(
            "symdesk-protocol-retry-{}-{}",
            std::process::id(),
            unix_nanos(SystemTime::now())
        ));
        let jobs = root.join(".symdesk/server/jobs");
        fs::create_dir_all(&jobs).unwrap();
        let id = "00000000000000000000000000000001";
        let path = jobs.join(format!("{id}.json"));
        fs::write(
            &path,
            format!(
                "{{\"id\":\"{id}\",\"schema_version\":1,\"status\":\"failed\",\"source_path\":\"inbox/a.pdf\",\"original_name\":\"a.pdf\",\"capability\":\"ocr\",\"worker_id\":\"worker-1\",\"error\":\"broken\",\"created_at\":\"2026-01-02T03:04:05Z\",\"updated_at\":\"2026-01-02T03:04:05Z\",\"lease_until\":\"2026-01-02T03:05:05Z\"}}"
            ),
        )
        .unwrap();
        let app = router(Arc::new(test_state(
            &root,
            "a sufficiently long test token",
        )));
        let response = app
            .oneshot(
                Request::builder()
                    .method(Method::POST)
                    .uri(format!("/api/v1/jobs/retry?id={id}"))
                    .header(
                        header::AUTHORIZATION,
                        "Bearer a sufficiently long test token",
                    )
                    .body(Body::empty())
                    .unwrap(),
            )
            .await
            .unwrap();
        assert_eq!(response.status(), StatusCode::OK);
        let response_job: serde_json::Value =
            serde_json::from_slice(&to_bytes(response.into_body(), 1 << 20).await.unwrap())
                .unwrap();
        assert_eq!(response_job["status"], "pending");
        assert!(response_job["worker_id"].is_null());
        assert!(response_job["error"].is_null());
        assert!(response_job["lease_until"].is_null());
        assert!(
            OffsetDateTime::parse(response_job["updated_at"].as_str().unwrap(), &Rfc3339).unwrap()
                > OffsetDateTime::parse("2026-01-02T03:04:05Z", &Rfc3339).unwrap()
        );

        let persisted_bytes = fs::read(&path).unwrap();
        assert_ne!(persisted_bytes.last(), Some(&b'\n'));
        let persisted: JobRecord = serde_json::from_slice(&persisted_bytes).unwrap();
        assert_eq!(persisted.status, "pending");
        assert!(persisted.worker_id.is_empty());
        assert!(persisted.error.is_empty());
        assert!(persisted.lease_until.is_none());
        assert_eq!(
            fs::metadata(&path).unwrap().permissions().mode() & 0o777,
            0o600
        );
        fs::remove_dir_all(root).unwrap();
    }

    #[cfg(unix)]
    struct RaceCleanup {
        root: PathBuf,
        outside: PathBuf,
        stop: Arc<std::sync::atomic::AtomicBool>,
        writer: Option<std::thread::JoinHandle<()>>,
    }

    #[cfg(unix)]
    impl Drop for RaceCleanup {
        fn drop(&mut self) {
            self.stop.store(true, std::sync::atomic::Ordering::SeqCst);
            if let Some(writer) = self.writer.take() {
                let _ = writer.join();
            }
            let _ = fs::remove_dir_all(&self.root);
            let _ = fs::remove_file(&self.outside);
        }
    }

    #[test]
    fn injected_reader_obeys_empty_and_small_buffers() {
        let mut reader = InjectedReadFailure::default();
        let mut empty = [];
        assert_eq!(reader.read(&mut empty).unwrap(), 0);
        let mut small = [0; 2];
        assert_eq!(reader.read(&mut small).unwrap(), 2);
        assert_eq!(&small, b"pa");
        assert!(reader.read(&mut [0; 1]).is_err());
    }

    #[tokio::test]
    async fn put_file_requires_auth_and_writes_bounded_markdown_atomically() {
        let root = std::env::temp_dir().join(format!(
            "symdesk-protocol-put-{}-{}",
            std::process::id(),
            unix_nanos(SystemTime::now())
        ));
        fs::create_dir(&root).expect("create test root");
        let app = router(Arc::new(test_state(
            &root,
            "a sufficiently long test token",
        )));

        let unauthorized = app
            .clone()
            .oneshot(
                Request::builder()
                    .method(Method::PUT)
                    .uri("/api/v1/files?path=notes/new.md")
                    .body(Body::from("note"))
                    .unwrap(),
            )
            .await
            .unwrap();
        assert_eq!(unauthorized.status(), StatusCode::UNAUTHORIZED);

        let response = app
            .clone()
            .oneshot(
                Request::builder()
                    .method(Method::PUT)
                    .uri("/api/v1/files?path=notes/new.MD")
                    .header(
                        header::AUTHORIZATION,
                        "Bearer a sufficiently long test token",
                    )
                    .body(Body::from("note putneedleunique"))
                    .unwrap(),
            )
            .await
            .unwrap();
        assert_eq!(response.status(), StatusCode::OK);
        assert_eq!(
            fs::read(root.join("notes/new.MD")).unwrap(),
            b"note putneedleunique"
        );
        let sidecar = Sidecar::open(&root.join(".symdesk/server/sidecar.db"))
            .expect("open self-hosted index");
        let hits = sidecar.search("putneedleunique").expect("search index");
        assert!(
            hits.iter()
                .any(|hit| { hit.path == root.join("notes/new.MD").to_string_lossy() })
        );
        drop(sidecar);

        let oversized = app
            .oneshot(
                Request::builder()
                    .method(Method::PUT)
                    .uri("/api/v1/files?path=notes/too-big.md")
                    .header(
                        header::AUTHORIZATION,
                        "Bearer a sufficiently long test token",
                    )
                    .body(Body::from(vec![b'x'; MAX_NOTE_BYTES as usize + 1]))
                    .unwrap(),
            )
            .await
            .unwrap();
        assert_eq!(oversized.status(), StatusCode::PAYLOAD_TOO_LARGE);
        assert!(!root.join("notes/too-big.md").exists());
        fs::remove_dir_all(root).unwrap();
    }

    #[cfg(unix)]
    #[tokio::test]
    async fn put_file_cannot_escape_through_a_parent_symlink() {
        use std::os::unix::fs::symlink;

        let root = std::env::temp_dir().join(format!(
            "symdesk-protocol-put-link-{}-{}",
            std::process::id(),
            unix_nanos(SystemTime::now())
        ));
        let outside = root.with_extension("outside");
        fs::create_dir(&root).expect("create test root");
        fs::create_dir(&outside).expect("create outside dir");
        fs::write(outside.join("sentinel.md"), b"outside").unwrap();
        symlink(&outside, root.join("linked")).unwrap();
        let app = router(Arc::new(test_state(
            &root,
            "a sufficiently long test token",
        )));
        let response = app
            .oneshot(
                Request::builder()
                    .method(Method::PUT)
                    .uri("/api/v1/files?path=linked/sentinel.md")
                    .header(
                        header::AUTHORIZATION,
                        "Bearer a sufficiently long test token",
                    )
                    .body(Body::from("changed"))
                    .unwrap(),
            )
            .await
            .unwrap();
        assert_eq!(response.status(), StatusCode::INTERNAL_SERVER_ERROR);
        assert_eq!(fs::read(outside.join("sentinel.md")).unwrap(), b"outside");
        fs::remove_dir_all(root).unwrap();
        fs::remove_dir_all(outside).unwrap();
    }

    #[tokio::test]
    async fn put_file_keeps_written_file_when_sidecar_indexing_fails() {
        let root = std::env::temp_dir().join(format!(
            "symdesk-protocol-put-index-failure-{}-{}",
            std::process::id(),
            unix_nanos(SystemTime::now())
        ));
        fs::create_dir_all(root.join(".symdesk")).expect("create internal dir");
        fs::write(root.join(".symdesk/server"), b"blocks sidecar directory")
            .expect("block sidecar dir");
        let app = router(Arc::new(test_state(
            &root,
            "a sufficiently long test token",
        )));
        let response = app
            .oneshot(
                Request::builder()
                    .method(Method::PUT)
                    .uri("/api/v1/files?path=notes/persisted.md")
                    .header(
                        header::AUTHORIZATION,
                        "Bearer a sufficiently long test token",
                    )
                    .body(Body::from("persisted indexedfailuretoken"))
                    .unwrap(),
            )
            .await
            .unwrap();
        assert_eq!(response.status(), StatusCode::INTERNAL_SERVER_ERROR);
        assert_eq!(
            fs::read(root.join("notes/persisted.md")).unwrap(),
            b"persisted indexedfailuretoken"
        );
        fs::remove_dir_all(root).unwrap();
    }

    #[tokio::test]
    async fn get_notebook_resolves_titles_and_marks_unavailable_sources_missing() {
        let root = std::env::temp_dir().join(format!(
            "symdesk-protocol-notebook-{}-{}",
            std::process::id(),
            unix_nanos(SystemTime::now())
        ));
        fs::create_dir_all(root.join("notebooks")).unwrap();
        fs::create_dir_all(root.join("notes")).unwrap();
        fs::write(
            root.join("notebooks/research.md"),
            "---\ntype: notebook\ntitle: Research\ncreated: 2026-01-02\nnotebook_id: research\nsources: [notes/hello.md, notes/missing.md, ../outside.md]\n---\n",
        )
        .unwrap();
        fs::write(
            root.join("notes/hello.md"),
            "---\ntitle: Hello\ncreated: 2026-01-01\n---\nprivate source content\n",
        )
        .unwrap();
        let outside = root.with_extension("outside.md");
        fs::write(&outside, "outside sentinel").unwrap();
        let app = router(Arc::new(test_state(
            &root,
            "a sufficiently long test token",
        )));
        let response = app
            .oneshot(
                Request::builder()
                    .uri("/api/v1/notebooks/research")
                    .header(
                        header::AUTHORIZATION,
                        "Bearer a sufficiently long test token",
                    )
                    .body(Body::empty())
                    .unwrap(),
            )
            .await
            .unwrap();
        assert_eq!(response.status(), StatusCode::OK);
        let body = to_bytes(response.into_body(), 1 << 20).await.unwrap();
        let body_text = String::from_utf8_lossy(&body);
        assert!(
            body_text.starts_with(
                r#"{"id":"research","path":"notebooks/research.md","title":"Research","created":"","sources":[{"path":"../outside.md"#
            ),
            "unexpected notebook JSON order: {body_text}"
        );
        let value: serde_json::Value = serde_json::from_slice(&body).unwrap();
        assert_eq!(value["id"], "research");
        assert_eq!(value["sources"][0]["path"], "../outside.md");
        assert_eq!(value["sources"][0]["missing"], true);
        assert_eq!(value["sources"][1]["title"], "Hello");
        assert!(value["sources"][1].get("missing").is_none());
        assert_eq!(value["sources"][2]["missing"], true);
        assert!(!body_text.contains("private source content"));
        fs::remove_dir_all(&root).unwrap();
        fs::remove_file(outside).unwrap();
    }

    #[tokio::test]
    async fn get_notebook_returns_not_found_for_unknown_id() {
        let root = std::env::temp_dir().join(format!(
            "symdesk-protocol-notebook-missing-{}-{}",
            std::process::id(),
            unix_nanos(SystemTime::now())
        ));
        fs::create_dir(&root).unwrap();
        let app = router(Arc::new(test_state(
            &root,
            "a sufficiently long test token",
        )));
        let response = app
            .oneshot(
                Request::builder()
                    .uri("/api/v1/notebooks/missing")
                    .header(
                        header::AUTHORIZATION,
                        "Bearer a sufficiently long test token",
                    )
                    .body(Body::empty())
                    .unwrap(),
            )
            .await
            .unwrap();
        assert_eq!(response.status(), StatusCode::NOT_FOUND);
        assert_eq!(
            to_bytes(response.into_body(), 1 << 20).await.unwrap(),
            br#"{"error":"notebook not found: notebook not found"}
"#
            .as_slice()
        );
        fs::remove_dir_all(root).unwrap();
    }

    #[cfg(unix)]
    #[test]
    fn notebook_file_reader_rejects_symlink_escape() {
        use std::os::unix::fs::symlink;

        let root = std::env::temp_dir().join(format!(
            "symdesk-protocol-notebook-link-{}-{}",
            std::process::id(),
            unix_nanos(SystemTime::now())
        ));
        let outside = root.with_extension("outside");
        fs::create_dir(&root).unwrap();
        fs::write(&outside, b"outside sentinel").unwrap();
        symlink(&outside, root.join("linked.md")).unwrap();
        let root_dir =
            cap_std::fs::Dir::open_ambient_dir(&root, cap_std::ambient_authority()).unwrap();
        assert!(read_root_file(&root_dir, Path::new("linked.md")).is_err());
        assert_eq!(fs::read(&outside).unwrap(), b"outside sentinel");
        fs::remove_dir_all(&root).unwrap();
        fs::remove_file(outside).unwrap();
    }

    #[test]
    fn notebook_file_reader_rejects_files_over_limit_before_reading() {
        let root = std::env::temp_dir().join(format!(
            "symdesk-protocol-notebook-large-{}-{}",
            std::process::id(),
            unix_nanos(SystemTime::now())
        ));
        fs::create_dir(&root).unwrap();
        let file = fs::File::create(root.join("large.md")).unwrap();
        file.set_len(MAX_NOTEBOOK_FILE_BYTES + 1).unwrap();
        drop(file);
        let root_dir =
            cap_std::fs::Dir::open_ambient_dir(&root, cap_std::ambient_authority()).unwrap();
        assert!(read_root_file(&root_dir, Path::new("large.md")).is_err());
        fs::remove_dir_all(root).unwrap();
    }

    fn test_state(root: &Path, token: &str) -> AppState {
        AppState {
            vault_root: root.to_path_buf(),
            token: Arc::from(token.as_bytes()),
            worker_token: None,
            version: String::new(),
            auth_failures: Mutex::new(AuthThrottle::default()),
            snapshot_cache: SnapshotCache::uncached(),
            job_retry: Mutex::new(()),
            share_write: Mutex::new(()),
        }
    }

    #[test]
    fn snapshot_path_normalization_matches_go_to_slash_without_corrupting_unix_names() {
        // Go 1.26.6 filepath.ToSlash replaces each native separator. It does
        // not clean repeated separators, dot segments, or drop components.
        for (input, windows) in [
            ("folder\\literal\\name.md", "folder/literal/name.md"),
            ("folder\\\\name.md", "folder//name.md"),
            ("folder\\.\\..\\name.md", "folder/./../name.md"),
            ("folder/mixed\\name.md", "folder/mixed/name.md"),
            ("folder//name.md", "folder//name.md"),
            ("folder\\", "folder/"),
            ("", ""),
        ] {
            let expected = if cfg!(windows) { windows } else { input };
            assert_eq!(
                normalize_snapshot_path(Path::new(input)),
                expected,
                "{input}"
            );
        }
    }

    #[test]
    fn constant_time_comparison_checks_length_without_shortcut() {
        assert!(constant_time_equal(b"token", b"token"));
        assert!(!constant_time_equal(b"token", b"tokens"));
        assert!(!constant_time_equal(b"token", b"tokEn"));
    }

    #[test]
    fn ranges_are_bounded_and_support_suffixes() {
        assert_eq!(parse_range("bytes=0-2", 5), Ok((0, 2)));
        assert_eq!(parse_range("bytes=2-", 5), Ok((2, 4)));
        assert_eq!(parse_range("bytes=-2", 5), Ok((3, 4)));
        assert!(parse_range("bytes=9-", 5).is_err());
        assert!(parse_range("bytes=0-1,2-3", 5).is_err());
    }

    #[test]
    fn representative_request_timeout_is_bounded() {
        assert_eq!(READ_TIMEOUT, Duration::from_secs(120));
    }

    #[test]
    fn aggregate_snapshot_budget_rejects_before_the_next_note_allocation() {
        let root = std::env::temp_dir().join(format!(
            "symdesk-protocol-budget-{}-{}",
            std::process::id(),
            unix_nanos(SystemTime::now())
        ));
        fs::create_dir(&root).expect("create test root");
        for name in ["a.md", "b.md"] {
            let file = fs::File::create(root.join(name)).expect("create note");
            file.set_len(MAX_NOTE_BYTES).expect("size note");
        }
        let state = AppState {
            vault_root: root.clone(),
            token: Arc::from(Vec::<u8>::new()),
            worker_token: None,
            version: String::new(),
            auth_failures: Mutex::new(AuthThrottle::default()),
            snapshot_cache: SnapshotCache::uncached(),
            job_retry: Mutex::new(()),
            share_write: Mutex::new(()),
        };

        assert!(matches!(
            snapshot_payload(&state),
            Err(error) if error == SNAPSHOT_TOO_LARGE
        ));
        fs::remove_dir_all(root).expect("remove test root");
    }

    #[test]
    fn aggregate_snapshot_budget_streams_many_small_notes() {
        let root = std::env::temp_dir().join(format!(
            "symdesk-protocol-many-notes-{}-{}",
            std::process::id(),
            unix_nanos(SystemTime::now())
        ));
        fs::create_dir(&root).expect("create test root");
        let content = vec![b'x'; 20 * 1024];
        for index in 0..1_024 {
            fs::write(root.join(format!("note-{index:04}.md")), &content).expect("write note");
        }
        let state = AppState {
            vault_root: root.clone(),
            token: Arc::from(Vec::<u8>::new()),
            worker_token: None,
            version: String::new(),
            auth_failures: Mutex::new(AuthThrottle::default()),
            snapshot_cache: SnapshotCache::uncached(),
            job_retry: Mutex::new(()),
            share_write: Mutex::new(()),
        };

        assert!(matches!(
            snapshot_payload(&state),
            Err(error) if error == SNAPSHOT_TOO_LARGE
        ));
        fs::remove_dir_all(root).expect("remove test root");
    }

    #[test]
    fn snapshot_plain_payload_bound_rejects_json_expansion() {
        let root = std::env::temp_dir().join(format!(
            "symdesk-protocol-plain-budget-{}-{}",
            std::process::id(),
            unix_nanos(SystemTime::now())
        ));
        fs::create_dir(&root).expect("create test root");
        fs::write(root.join("quoted.md"), vec![b'"'; MAX_NOTE_BYTES as usize])
            .expect("write quoted note");
        let state = AppState {
            vault_root: root.clone(),
            token: Arc::from(Vec::<u8>::new()),
            worker_token: None,
            version: String::new(),
            auth_failures: Mutex::new(AuthThrottle::default()),
            snapshot_cache: SnapshotCache::uncached(),
            job_retry: Mutex::new(()),
            share_write: Mutex::new(()),
        };

        assert!(matches!(
            snapshot_payload(&state),
            Err(error) if error == SNAPSHOT_TOO_LARGE
        ));
        fs::remove_dir_all(root).expect("remove test root");
    }

    #[test]
    fn authentication_failures_match_go_threshold_and_block() {
        let mut throttle = AuthThrottle::default();
        for _ in 0..4 {
            assert!(throttle.record("127.0.0.1").is_none());
        }
        assert_eq!(throttle.record("127.0.0.1"), Some(AUTH_BLOCK));
        assert!(throttle.record("127.0.0.1").is_some());
        assert!(throttle.record("127.0.0.2").is_none());
        assert_eq!(retry_after_seconds(Duration::from_millis(1_001)), 2);
        assert_eq!(retry_after_seconds(Duration::from_secs(2)), 2);
    }

    #[test]
    fn share_failures_use_an_independent_bucket() {
        let mut throttle = AuthThrottle::default();
        for _ in 0..AUTH_MAX {
            throttle.record("127.0.0.1");
        }
        assert!(throttle.record_share("127.0.0.1").is_none());
        assert!(throttle.record_share("127.0.0.1").is_none());
        assert_eq!(throttle.record_share("127.0.0.1"), Some(SHARE_BLOCK));
        assert!(throttle.record("127.0.0.1").is_some());
    }

    #[test]
    fn ai_requests_use_a_separate_go_sized_bucket() {
        let mut throttle = AuthThrottle::default();
        for _ in 0..AUTH_MAX {
            throttle.record("127.0.0.1");
        }
        for _ in 0..AI_MAX - 1 {
            assert!(throttle.record_ai("127.0.0.1").is_none());
        }
        assert_eq!(throttle.record_ai("127.0.0.1"), Some(AI_BLOCK));
        assert!(throttle.record_ai("127.0.0.1").is_some());
        assert_eq!(throttle.record_ai("127.0.0.2"), None);
    }

    #[test]
    fn transform_fallback_only_runs_without_a_configured_provider() {
        let default = symdesk_core::config::Config::default();
        assert_eq!(
            ai_transform_fallback_text(&default),
            Some(
                "⚠️ **AI feature not configured.**\n\nSet your Ollama endpoint in Settings → AI.\n"
            )
        );

        let mut ollama = default.clone();
        ollama.ollama_url = "http://127.0.0.1:11434".to_owned();
        assert_eq!(ai_transform_fallback_text(&ollama), None);

        let anthropic_without_key = symdesk_core::config::load(
            None,
            &BTreeMap::from([(
                String::from("SYMDESK_LLM_PROVIDER"),
                String::from("anthropic"),
            )]),
        )
        .expect("load unconfigured Anthropic provider");
        assert_eq!(
            ai_transform_fallback_text(&anthropic_without_key),
            Some(
                "⚠️ **AI feature not configured.**\n\nAnthropic API key could not be resolved (missing secret via symvault or environment variable).\n"
            )
        );

        let anthropic_with_key = symdesk_core::config::load(
            None,
            &BTreeMap::from([
                (
                    String::from("SYMDESK_LLM_PROVIDER"),
                    String::from("anthropic"),
                ),
                (
                    String::from("SYMDESK_LLM_API_KEY"),
                    String::from("test-key"),
                ),
            ]),
        )
        .expect("load configured Anthropic provider");
        assert_eq!(ai_transform_fallback_text(&anthropic_with_key), None);

        let env_overrides_file = symdesk_core::config::load(
            Some("llm_provider = \"anthropic\"\nllm_api_key = \"file-key\"\n"),
            &BTreeMap::from([(String::from("SYMDESK_LLM_PROVIDER"), String::from("ollama"))]),
        )
        .expect("load config with environment override");
        assert_eq!(
            ai_transform_fallback_text(&env_overrides_file),
            Some(
                "⚠️ **AI feature not configured.**\n\nSet your Ollama endpoint in Settings → AI.\n"
            ),
            "environment provider must take precedence over TOML"
        );

        let failed_load = ai_transform_config_or_default(Err("invalid TOML".to_owned()));
        assert_eq!(
            ai_transform_fallback_text(&failed_load),
            ai_transform_fallback_text(&default),
            "Go's service constructor uses DefaultConfig when config.Load fails"
        );
    }

    #[test]
    fn confined_paths_match_fs_valid_path_rules() {
        assert!(confined_path(&dummy_state(), ".").is_ok());
        for invalid in ["a//b", "a/./b", "a/../b", "a/", "/a", ""] {
            assert!(
                confined_path(&dummy_state(), invalid).is_err(),
                "expected invalid path: {invalid:?}"
            );
        }
    }

    fn dummy_state() -> AppState {
        AppState {
            vault_root: PathBuf::new(),
            token: Arc::from(Vec::<u8>::new()),
            worker_token: None,
            version: String::new(),
            auth_failures: Mutex::new(AuthThrottle::default()),
            snapshot_cache: SnapshotCache::uncached(),
            job_retry: Mutex::new(()),
            share_write: Mutex::new(()),
        }
    }

    #[test]
    fn range_parser_rejects_overflow_without_unbounded_allocation() {
        assert!(parse_range("bytes=18446744073709551616-", 5).is_err());
        assert!(parse_range("bytes=-18446744073709551616", 5).is_err());
        assert_eq!(parse_range("bytes=0-18446744073709551615", 5), Ok((0, 4)));
    }

    #[test]
    fn content_type_preserves_original_extension_for_exact_then_lower_lookup() {
        let result = content_type_with_mime_loader(Path::new("note.MD"), b"plain text", |ext| {
            assert_eq!(ext, ".MD");
            None
        });
        assert_eq!(result, "text/plain; charset=utf-8");
    }

    #[test]
    fn content_type_ignores_empty_mime_loader_result_and_falls_back() {
        assert_eq!(
            content_type_with_mime_loader(Path::new("note.md"), b"plain text", |_| {
                Some(String::new())
            }),
            "text/plain; charset=utf-8"
        );
    }

    #[test]
    fn content_type_uses_injected_mime_loader_before_fallbacks() {
        let calls = std::sync::Mutex::new(Vec::new());
        let result = content_type_with_mime_loader(Path::new("note.md"), b"plain text", |ext| {
            calls.lock().unwrap().push(ext.to_owned());
            Some("text/markdown; charset=utf-8".to_owned())
        });
        assert_eq!(result, "text/markdown; charset=utf-8");
        assert_eq!(&*calls.lock().unwrap(), &[".md"]);
    }

    #[test]
    fn serve_content_mime_fallback_matches_representative_types() {
        assert_eq!(
            content_type(Path::new("data.json"), b"{}"),
            "application/json"
        );
        assert_eq!(
            content_type(Path::new("asset.bin"), &[0, 1, 2]),
            "application/octet-stream"
        );
        assert_eq!(detect_content_type(&[]), "text/plain; charset=utf-8");
    }

    #[cfg(unix)]
    #[test]
    fn opened_root_allows_internal_but_rejects_external_symlinks() {
        use std::os::unix::fs::symlink;

        let root = std::env::temp_dir().join(format!(
            "symdesk-protocol-symlink-{}-{}",
            std::process::id(),
            unix_nanos(SystemTime::now())
        ));
        let outside = root.with_extension("outside");
        fs::create_dir(&root).expect("create test root");
        fs::write(root.join("inside.md"), b"inside").expect("write inside");
        fs::write(&outside, b"outside").expect("write outside");
        symlink("inside.md", root.join("internal.md")).expect("create internal link");
        symlink(&outside, root.join("external.md")).expect("create external link");

        let dir = cap_std::fs::Dir::open_ambient_dir(&root, cap_std::ambient_authority())
            .expect("open test root");
        let mut internal = dir.open("internal.md").expect("open internal link");
        let mut content = String::new();
        internal
            .read_to_string(&mut content)
            .expect("read internal link");
        assert_eq!(content, "inside");
        assert!(dir.open("external.md").is_err());

        fs::remove_dir_all(&root).expect("remove test root");
        fs::remove_file(outside).expect("remove outside file");
    }

    #[cfg(unix)]
    #[test]
    fn opened_root_rejects_external_symlink_replacement_races() {
        use std::{
            os::unix::fs::symlink,
            sync::{
                Arc,
                atomic::{AtomicBool, Ordering},
            },
            thread,
        };

        let root = std::env::temp_dir().join(format!(
            "symdesk-protocol-race-{}-{}",
            std::process::id(),
            unix_nanos(SystemTime::now())
        ));
        let outside = root.with_extension("outside");
        fs::create_dir(&root).expect("create test root");
        fs::write(root.join("inside.md"), b"inside").expect("write inside");
        fs::write(&outside, b"outside").expect("write outside");
        let raced = root.join("raced.md");
        let replacement = root.join("raced.replacement");
        symlink("inside.md", &raced).expect("create initial internal link");

        let dir = Arc::new(
            cap_std::fs::Dir::open_ambient_dir(&root, cap_std::ambient_authority())
                .expect("open test root"),
        );
        let stop = Arc::new(AtomicBool::new(false));
        let writer_stop = Arc::clone(&stop);
        let writer_root = root.clone();
        let writer = thread::spawn(move || {
            for iteration in 0..20_000 {
                if writer_stop.load(Ordering::SeqCst) {
                    break;
                }
                let target = if iteration % 2 == 0 {
                    "inside.md".to_owned()
                } else {
                    writer_root
                        .with_extension("outside")
                        .to_string_lossy()
                        .into_owned()
                };
                let _ = fs::remove_file(&replacement);
                if symlink(target, &replacement).is_err() {
                    continue;
                }
                if fs::rename(&replacement, &raced).is_err() {
                    let _ = fs::remove_file(&replacement);
                }
            }
        });
        let mut cleanup = RaceCleanup {
            root,
            outside,
            stop,
            writer: Some(writer),
        };

        for _ in 0..20_000 {
            let Ok(mut file) = dir.open("raced.md") else {
                continue;
            };
            let mut bytes = Vec::new();
            match file.read_to_end(&mut bytes) {
                Ok(_) => assert_eq!(bytes, b"inside", "unexpected snapshot bytes"),
                Err(_) => assert!(
                    bytes.is_empty() || b"inside".starts_with(&bytes),
                    "partial read disclosed outside bytes"
                ),
            }
        }
        cleanup.stop.store(true, Ordering::SeqCst);
        cleanup.writer.take().unwrap().join().unwrap();
    }
}
