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

use snapshot_cache::{SnapshotCache, SnapshotPayload};

use std::{
    fmt::Write as _,
    fs,
    io::{self, Read, Seek, SeekFrom},
    net::SocketAddr,
    path::{Component, Path, PathBuf},
    sync::{Arc, Mutex},
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
const READ_TIMEOUT: Duration = Duration::from_secs(120);

#[derive(Clone, Debug)]
pub struct HttpConfig {
    pub listen_address: String,
    pub vault_root: PathBuf,
    pub token: String,
    pub version: String,
}

struct AppState {
    vault_root: PathBuf,
    token: Arc<[u8]>,
    version: String,
    auth_failures: Mutex<AuthThrottle>,
    snapshot_cache: SnapshotCache,
}

#[derive(Debug, Default)]
struct AuthThrottle {
    entries: std::collections::HashMap<String, AuthFailure>,
}

#[derive(Debug)]
struct AuthFailure {
    count: u32,
    window_start: SystemTime,
    blocked_until: SystemTime,
    last_seen: SystemTime,
}

const AUTH_WINDOW: Duration = Duration::from_secs(10);
const AUTH_BLOCK: Duration = Duration::from_secs(30);
const AUTH_MAX: u32 = 5;
const AUTH_MAX_ENTRIES: usize = 5_000;

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
    if config.token.len() < 32 {
        return Err("server token must contain at least 32 characters".to_owned());
    }
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
        version: config.version,
        auth_failures: Mutex::new(AuthThrottle::default()),
        snapshot_cache: SnapshotCache::new(&root),
    });
    let app = router(state);
    eprintln!("LISTENING http://{actual}");

    axum::serve(
        listener,
        app.into_make_service_with_connect_info::<SocketAddr>(),
    )
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
        .map(|value| value.trim().strip_prefix("Bearer ").unwrap_or(value).trim())
        .unwrap_or_default();
    if !constant_time_equal(provided.as_bytes(), &state.token) {
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
    }
    next.run(request).await
}

fn client_ip(request: &Request<Body>) -> String {
    request
        .extensions()
        .get::<axum::extract::ConnectInfo<SocketAddr>>()
        .map(|info| info.0.ip().to_string())
        .unwrap_or_default()
}

impl AuthThrottle {
    fn record(&mut self, ip: &str) -> Option<Duration> {
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
        let failure = self.entries.entry(ip.to_owned()).or_insert(AuthFailure {
            count: 0,
            window_start: now,
            blocked_until: UNIX_EPOCH,
            last_seen: now,
        });
        failure.last_seen = now;
        if failure.blocked_until > now {
            return Some(
                failure
                    .blocked_until
                    .duration_since(now)
                    .unwrap_or_default(),
            );
        }
        if now
            .duration_since(failure.window_start)
            .map(|age| age > AUTH_WINDOW)
            .unwrap_or(true)
        {
            failure.count = 0;
            failure.window_start = now;
        }
        failure.count = failure.count.saturating_add(1);
        if failure.count >= AUTH_MAX {
            failure.blocked_until = now + AUTH_BLOCK;
            return Some(AUTH_BLOCK);
        }
        None
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
    let payload = match state.snapshot_cache.get_or_build(
        || current_root_identity(&state),
        || snapshot_payload(&state),
    ) {
        Ok(value) => value,
        Err(error) => return json_error(StatusCode::INTERNAL_SERVER_ERROR, &error),
    };
    let SnapshotPayload {
        plain,
        compressed,
        etag,
    } = payload.as_ref();
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
    let relative = match confined_path(&state, requested) {
        Ok(path) => path,
        Err(PathError::Invalid) => {
            return json_error(StatusCode::BAD_REQUEST, "a vault-relative path is required");
        }
    };
    let root_dir = match open_current_root(&state) {
        Ok(root_dir) => root_dir,
        Err(_) => return json_error(StatusCode::NOT_FOUND, "file not found"),
    };
    let mut file = match root_dir.open(&relative) {
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
    let sample = match read_sample(&mut file, length) {
        Ok(sample) => sample,
        Err(_) => return json_error(StatusCode::NOT_FOUND, "file not found"),
    };
    let mut common = vec![
        (header::CONTENT_TYPE, content_type(&relative, &sample)),
        (
            header::CONTENT_DISPOSITION,
            format!("inline; filename=\"{}\"", safe_filename(&relative)),
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

    let range = if length == 0 {
        None
    } else {
        match headers
            .get(header::RANGE)
            .and_then(|value| value.to_str().ok())
            .map(|value| parse_range(value, length))
        {
            Some(Ok(range)) => Some(range),
            Some(Err(RangeError::Invalid)) => {
                return range_error_response(&relative, "invalid range", None);
            }
            Some(Err(RangeError::NoOverlap)) => {
                return range_error_response(
                    &relative,
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
    if body_length > MAX_NOTE_BYTES {
        return json_error(
            StatusCode::PAYLOAD_TOO_LARGE,
            "file exceeds the response size limit",
        );
    }
    let start = range.map_or(0, |(start, _)| start);
    let body = match read_at(&mut file, start, body_length) {
        Ok(body) => body,
        Err(_) => return json_error(StatusCode::NOT_FOUND, "file not found"),
    };
    bytes_response(status, common, body)
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

fn current_root_identity(state: &AppState) -> Option<String> {
    let root = open_current_root(state).ok()?;
    let metadata = root.dir_metadata().ok()?;
    #[cfg(unix)]
    {
        use cap_std::fs::MetadataExt;
        Some(format!("{}:{}", metadata.dev(), metadata.ino()))
    }
    #[cfg(not(unix))]
    {
        Some(format!("{metadata:?}"))
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
    let root_dir = open_current_root(state)?;
    for relative in walk_markdown(&state.vault_root).map_err(|error| error.to_string())? {
        // Preserve the pre-cache adapter's explicit rejection rather than
        // silently aliasing invalid paths to empty or replacement strings.
        relative
            .to_str()
            .ok_or_else(|| "vault path is not UTF-8".to_owned())?;
        let logical_path = normalize_snapshot_path(&relative);
        let mut file = match root_dir.open(&relative) {
            Ok(file) => file,
            // An external symlink, a concurrently removed file, and a file
            // replaced by an escaping symlink are all intentionally omitted.
            // The opened capability is the security boundary; no path is read
            // again after this point.
            Err(_) => continue,
        };
        let metadata = match file.metadata() {
            Ok(metadata) if metadata.is_file() && metadata.len() <= MAX_NOTE_BYTES => metadata,
            _ => continue,
        };
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
        read_result.map_err(|error| error.to_string())?;
        if bytes.len() as u64 > MAX_NOTE_BYTES {
            continue;
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
    }
    let etag = symdesk_vault::sha256_hex(etag_material.as_bytes());
    let snapshot = Snapshot {
        generated_at: format_rfc3339(SystemTime::now()),
        notes: files,
    };
    let mut plain = serde_json::to_vec(&snapshot).map_err(|error| error.to_string())?;
    plain.push(b'\n');
    let mut encoder = GzEncoder::new(Vec::new(), Compression::default());
    std::io::Write::write_all(&mut encoder, &plain).map_err(|error| error.to_string())?;
    let compressed = encoder.finish().map_err(|error| error.to_string())?;
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
            version: String::new(),
            auth_failures: Mutex::new(AuthThrottle::default()),
            snapshot_cache: SnapshotCache::uncached(),
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
