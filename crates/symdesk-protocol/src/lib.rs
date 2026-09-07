#![deny(unsafe_code)]

//! Compatibility-focused HTTP adapter for the representative SymDesk API.
//!
//! The adapter deliberately owns the public wire contract instead of exposing
//! Axum defaults: authentication, headers, path confinement, snapshots, and
//! file ranges are all tested at the HTTP boundary.

use std::{
    fmt::Write as _,
    fs,
    net::SocketAddr,
    path::{Component, Path, PathBuf},
    sync::Arc,
    time::{Duration, SystemTime, UNIX_EPOCH},
};

use axum::{
    Router,
    body::Body,
    extract::{Query, State},
    http::{HeaderMap, HeaderValue, Method, Request, StatusCode, header},
    middleware::{self, Next},
    response::Response,
    routing::get,
};
use flate2::{Compression, write::GzEncoder};
use httpdate::{fmt_http_date, parse_http_date};
use serde::{Deserialize, Serialize};
use serde_json::json;
use symdesk_vault::walk_markdown;
use time::{OffsetDateTime, format_description::well_known::Rfc3339};

const MAX_NOTE_BYTES: u64 = 8 << 20;
const READ_HEADER_TIMEOUT: Duration = Duration::from_secs(10);
const READ_TIMEOUT: Duration = Duration::from_secs(120);
const WRITE_TIMEOUT: Duration = Duration::from_secs(300);
const IDLE_TIMEOUT: Duration = Duration::from_secs(90);

#[derive(Clone, Debug)]
pub struct HttpConfig {
    pub listen_address: String,
    pub vault_root: PathBuf,
    pub token: String,
    pub version: String,
}

#[derive(Clone)]
struct AppState {
    vault_root: PathBuf,
    canonical_root: PathBuf,
    token: Arc<[u8]>,
    version: String,
}

#[derive(Debug, Deserialize)]
struct FileQuery {
    path: Option<String>,
}

#[derive(Debug, Serialize)]
struct Snapshot {
    generated_at: String,
    notes: Vec<SnapshotNote>,
}

#[derive(Debug, Serialize)]
struct SnapshotNote {
    path: String,
    content: String,
    modified_at: String,
}

#[derive(Debug)]
enum PathError {
    Invalid,
    NotFound,
    Internal,
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
    if config.token.len() < 32 {
        return Err("server token must contain at least 32 characters".to_owned());
    }
    let address: SocketAddr = config
        .listen_address
        .parse()
        .map_err(|error| format!("invalid listen address: {error}"))?;
    // Axum/Hyper bounds header parsing at the transport layer; the adapter
    // adds the longer body/handler deadlines explicitly here.
    let _transport_bounds = (READ_HEADER_TIMEOUT, WRITE_TIMEOUT, IDLE_TIMEOUT);
    let listener = tokio::net::TcpListener::bind(address)
        .await
        .map_err(|error| format!("bind HTTP listener: {error}"))?;
    let actual = listener
        .local_addr()
        .map_err(|error| format!("read HTTP listener address: {error}"))?;
    let state = Arc::new(AppState {
        vault_root: root.clone(),
        canonical_root: root,
        token: Arc::from(config.token.into_bytes()),
        version: config.version,
    });
    let app = router(state);
    eprintln!("LISTENING http://{actual}");

    axum::serve(listener, app)
        .with_graceful_shutdown(shutdown_signal())
        .await
        .map_err(|error| format!("HTTP server: {error}"))
}

fn router(state: Arc<AppState>) -> Router {
    let protected = Router::new()
        .route("/api/v1/status", get(handle_status))
        .route("/api/v1/snapshot", get(handle_snapshot))
        .route("/api/v1/files", get(handle_file))
        .layer(middleware::from_fn_with_state(
            Arc::clone(&state),
            authenticate,
        ));
    Router::new()
        .route("/healthz", get(handle_health))
        .merge(protected)
        .fallback(handle_not_found)
        .layer(middleware::from_fn(normalize_method_not_allowed))
        .layer(middleware::from_fn(request_timeout))
        .layer(middleware::from_fn(security_headers))
        .with_state(state)
}

async fn authenticate(
    State(state): State<Arc<AppState>>,
    request: Request<Body>,
    next: Next,
) -> Response {
    let provided = request
        .headers()
        .get(header::AUTHORIZATION)
        .and_then(|value| value.to_str().ok())
        .map(|value| value.strip_prefix("Bearer ").unwrap_or(value).trim())
        .unwrap_or_default();
    if !constant_time_equal(provided.as_bytes(), &state.token) {
        let mut response = json_error(StatusCode::UNAUTHORIZED, "authentication required");
        response
            .headers_mut()
            .insert(header::WWW_AUTHENTICATE, HeaderValue::from_static("Bearer"));
        return response;
    }
    next.run(request).await
}

async fn security_headers(request: Request<Body>, next: Next) -> Response {
    let mut response = next.run(request).await;
    let headers = response.headers_mut();
    headers.insert(
        header::X_CONTENT_TYPE_OPTIONS,
        HeaderValue::from_static("nosniff"),
    );
    headers.insert(
        header::REFERRER_POLICY,
        HeaderValue::from_static("no-referrer"),
    );
    headers.insert(header::CACHE_CONTROL, HeaderValue::from_static("no-store"));
    headers.insert(
        header::CONTENT_SECURITY_POLICY,
        HeaderValue::from_static("default-src 'none'; frame-ancestors 'none'"),
    );
    response
}

async fn normalize_method_not_allowed(request: Request<Body>, next: Next) -> Response {
    let mut response = next.run(request).await;
    if response.status() == StatusCode::METHOD_NOT_ALLOWED {
        response.headers_mut().remove(header::CONTENT_TYPE);
        response.headers_mut().insert(
            header::CONTENT_TYPE,
            HeaderValue::from_static("text/plain; charset=utf-8"),
        );
        response
            .headers_mut()
            .insert(header::ALLOW, HeaderValue::from_static("GET, HEAD"));
        *response.body_mut() = Body::from(b"Method Not Allowed\n".to_vec());
    }
    response
}

async fn request_timeout(request: Request<Body>, next: Next) -> Response {
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

async fn handle_snapshot(
    State(state): State<Arc<AppState>>,
    headers: HeaderMap,
    method: Method,
) -> Response {
    let (plain, compressed, etag) = match snapshot_payload(&state) {
        Ok(value) => value,
        Err(error) => return json_error(StatusCode::INTERNAL_SERVER_ERROR, &error),
    };
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
    if method == Method::HEAD {
        common.push((header::CONTENT_LENGTH, plain.len().to_string()));
        return bytes_response(StatusCode::OK, common, Vec::new());
    }
    if headers
        .get(header::ACCEPT_ENCODING)
        .and_then(|value| value.to_str().ok())
        .is_some_and(|value| value.split(',').any(|part| part.trim() == "gzip"))
    {
        common.push((header::CONTENT_ENCODING, "gzip".to_owned()));
        common.push((header::CONTENT_LENGTH, compressed.len().to_string()));
        return bytes_response(StatusCode::OK, common, compressed);
    }
    common.push((header::CONTENT_LENGTH, plain.len().to_string()));
    bytes_response(StatusCode::OK, common, plain)
}

async fn handle_file(
    State(state): State<Arc<AppState>>,
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
    let path = match confined_path(&state, requested) {
        Ok(path) => path,
        Err(PathError::Invalid) => {
            return json_error(StatusCode::BAD_REQUEST, "a vault-relative path is required");
        }
        Err(PathError::NotFound) => {
            return json_error(StatusCode::NOT_FOUND, "file not found");
        }
        Err(PathError::Internal) => {
            return json_error(StatusCode::INTERNAL_SERVER_ERROR, "path resolution failed");
        }
    };
    let metadata = match fs::metadata(&path) {
        Ok(metadata) if metadata.is_file() => metadata,
        _ => return json_error(StatusCode::NOT_FOUND, "file not found"),
    };
    let bytes = match fs::read(&path) {
        Ok(bytes) => bytes,
        Err(_) => return json_error(StatusCode::NOT_FOUND, "file not found"),
    };
    let modified = metadata.modified().unwrap_or(UNIX_EPOCH);
    let mut common = vec![
        (header::CONTENT_TYPE, content_type(&path)),
        (
            header::CONTENT_DISPOSITION,
            format!("inline; filename=\"{}\"", safe_filename(&path)),
        ),
        (header::ACCEPT_RANGES, "bytes".to_owned()),
        (header::LAST_MODIFIED, fmt_http_date(modified)),
    ];
    if let Some(value) = headers
        .get(header::IF_MODIFIED_SINCE)
        .and_then(|value| value.to_str().ok())
        .and_then(|value| parse_http_date(value).ok())
        && modified <= value
    {
        return bytes_response(StatusCode::NOT_MODIFIED, common, Vec::new());
    }

    let range = match headers
        .get(header::RANGE)
        .and_then(|value| value.to_str().ok())
        .map(|value| parse_range(value, bytes.len()))
    {
        Some(Ok(range)) => Some(range),
        Some(Err(())) => {
            common.push((header::CONTENT_RANGE, format!("bytes */{}", bytes.len())));
            return bytes_response(StatusCode::RANGE_NOT_SATISFIABLE, common, Vec::new());
        }
        None => None,
    };
    let (status, body, content_range) = if let Some((start, end)) = range {
        (
            StatusCode::PARTIAL_CONTENT,
            bytes[start..=end].to_vec(),
            Some(format!("bytes {start}-{end}/{}", bytes.len())),
        )
    } else {
        (StatusCode::OK, bytes, None)
    };
    if let Some(content_range) = content_range {
        common.push((header::CONTENT_RANGE, content_range));
    }
    common.push((header::CONTENT_LENGTH, body.len().to_string()));
    let body = if method == Method::HEAD {
        Vec::new()
    } else {
        body
    };
    bytes_response(status, common, body)
}

async fn handle_not_found() -> Response {
    bytes_response(
        StatusCode::NOT_FOUND,
        vec![(header::CONTENT_TYPE, "text/plain; charset=utf-8".to_owned())],
        b"404 page not found\n".to_vec(),
    )
}

fn snapshot_payload(state: &AppState) -> Result<(Vec<u8>, Vec<u8>, String), String> {
    let mut files = Vec::new();
    let mut etag_material = String::new();
    for relative in walk_markdown(&state.vault_root).map_err(|error| error.to_string())? {
        let relative = relative
            .to_str()
            .ok_or_else(|| "vault path is not UTF-8".to_owned())?;
        let path = state.vault_root.join(relative);
        let metadata = fs::metadata(&path).map_err(|error| error.to_string())?;
        if !metadata.is_file() || metadata.len() > MAX_NOTE_BYTES {
            continue;
        }
        let canonical = fs::canonicalize(&path).map_err(|error| error.to_string())?;
        if !canonical.starts_with(&state.canonical_root) {
            continue;
        }
        let bytes = fs::read(&path).map_err(|error| error.to_string())?;
        if bytes.len() as u64 > MAX_NOTE_BYTES {
            continue;
        }
        let modified = metadata.modified().unwrap_or(UNIX_EPOCH);
        let mtime_ns = unix_nanos(modified);
        let _ = writeln!(etag_material, "{relative}\0{}\0{mtime_ns}", bytes.len());
        files.push(SnapshotNote {
            path: relative.replace('\\', "/"),
            content: String::from_utf8_lossy(&bytes).into_owned(),
            modified_at: format_rfc3339(modified),
        });
    }
    let etag = symdesk_vault::sha256_hex(etag_material.as_bytes());
    let snapshot = Snapshot {
        notes: files,
        generated_at: format_rfc3339(SystemTime::now()),
    };
    let mut plain = serde_json::to_vec(&snapshot).map_err(|error| error.to_string())?;
    plain.push(b'\n');
    let mut encoder = GzEncoder::new(Vec::new(), Compression::default());
    std::io::Write::write_all(&mut encoder, &plain).map_err(|error| error.to_string())?;
    let compressed = encoder.finish().map_err(|error| error.to_string())?;
    Ok((plain, compressed, etag))
}

fn confined_path(state: &AppState, requested: &str) -> Result<PathBuf, PathError> {
    if requested.is_empty()
        || requested.contains('\0')
        || requested.contains('\\')
        || Path::new(requested).is_absolute()
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
    if contains_symlink(&state.vault_root, relative).map_err(|_| PathError::Internal)? {
        return Err(PathError::NotFound);
    }
    let candidate = state.vault_root.join(relative);
    let canonical = canonicalize_missing(&candidate).map_err(|_| PathError::Internal)?;
    if !canonical.starts_with(&state.canonical_root) {
        return Err(PathError::NotFound);
    }
    Ok(candidate)
}

fn contains_symlink(root: &Path, relative: &Path) -> std::io::Result<bool> {
    let mut current = root.to_path_buf();
    for component in relative.components() {
        let Component::Normal(part) = component else {
            continue;
        };
        current.push(part);
        match fs::symlink_metadata(&current) {
            Ok(metadata) if metadata.file_type().is_symlink() => return Ok(true),
            Ok(_) => {}
            Err(error) if error.kind() == std::io::ErrorKind::NotFound => return Ok(false),
            Err(error) => return Err(error),
        }
    }
    Ok(false)
}

fn canonicalize_missing(path: &Path) -> std::io::Result<PathBuf> {
    if path.exists() {
        return fs::canonicalize(path);
    }
    let mut parent = path.to_path_buf();
    while !parent.exists() {
        let Some(next) = parent.parent() else {
            return Err(std::io::Error::new(
                std::io::ErrorKind::NotFound,
                "no parent",
            ));
        };
        if next == parent {
            return Err(std::io::Error::new(
                std::io::ErrorKind::NotFound,
                "no parent",
            ));
        }
        parent = next.to_path_buf();
    }
    let canonical_parent = fs::canonicalize(&parent)?;
    let remainder = path.strip_prefix(&parent).unwrap_or(Path::new(""));
    Ok(canonical_parent.join(remainder))
}

fn parse_range(value: &str, length: usize) -> Result<(usize, usize), ()> {
    let value = value.strip_prefix("bytes=").ok_or(())?;
    if value.contains(',') || length == 0 {
        return Err(());
    }
    let (start, end) = value.split_once('-').ok_or(())?;
    if start.is_empty() {
        let suffix = end.parse::<usize>().map_err(|_| ())?;
        if suffix == 0 {
            return Err(());
        }
        return Ok((length.saturating_sub(suffix), length - 1));
    }
    let start = start.parse::<usize>().map_err(|_| ())?;
    if start >= length {
        return Err(());
    }
    let end = if end.is_empty() {
        length - 1
    } else {
        end.parse::<usize>().map_err(|_| ())?.min(length - 1)
    };
    if start > end {
        return Err(());
    }
    Ok((start, end))
}

fn content_type(path: &Path) -> String {
    if path.extension().and_then(|ext| ext.to_str()) == Some("md") {
        "text/plain; charset=utf-8".to_owned()
    } else {
        "application/octet-stream".to_owned()
    }
}

fn safe_filename(path: &Path) -> String {
    let value = path
        .file_name()
        .and_then(|name| name.to_str())
        .unwrap_or("document.bin")
        .trim();
    if value.is_empty() || value == "." {
        "document.bin".to_owned()
    } else {
        value.replace('"', "_")
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

fn json_response(status: StatusCode, value: serde_json::Value) -> Response {
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
    body: Vec<u8>,
) -> Response {
    let mut response = Response::new(Body::from(body));
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
    fn timeout_constants_keep_server_bounds_explicit() {
        assert!(READ_HEADER_TIMEOUT < READ_TIMEOUT);
        assert!(READ_TIMEOUT < WRITE_TIMEOUT);
        assert!(IDLE_TIMEOUT < WRITE_TIMEOUT);
    }
}
