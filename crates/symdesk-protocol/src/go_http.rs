//! Go `net/http` front-door behavior the adapter reproduces on the wire.
//!
//! The Go server sits behind `http.Server` and `http.ServeMux`. Both make
//! observable decisions before any SymDesk handler (and before its
//! authentication middleware) runs:
//!
//! * `Server` rejects requests with a missing, repeated or malformed `Host`,
//!   unsupported transfer codings, unknown `Expect` values and oversized
//!   header blocks;
//! * `ServeMux` cleans the path (307 redirect), rejects malformed URL escapes,
//!   matches routes on percent-decoded segments, and answers 404/405 itself,
//!   so an unauthenticated request with the wrong method gets `405`, not `401`;
//! * `Request.URL.Query()` silently drops pairs containing `;` or a malformed
//!   escape and returns the first value of a repeated key.
//!
//! Everything here is a plain function over request metadata so it can be
//! unit-tested without a socket.

use axum::{
    body::Body,
    extract::{FromRequestParts, Request},
    http::{HeaderValue, Method, StatusCode, Version, header, request::Parts},
    middleware::Next,
    response::Response,
};
use hyper::ext::ReasonPhrase;

/// Go's `Server.MaxHeaderBytes` (1 MiB) plus the 4096 byte slop Go adds to the
/// read limit of the request line and header block.
pub(crate) const MAX_HEADER_BLOCK_BYTES: usize = (1 << 20) + 4096;

/// Reason phrases of `http.StatusText` that differ from the `http` crate's.
pub(crate) fn go_reason_phrase(status: StatusCode) -> Option<&'static str> {
    match status.as_u16() {
        203 => Some("Non-Authoritative Information"),
        413 => Some("Request Entity Too Large"),
        414 => Some("Request URI Too Long"),
        416 => Some("Requested Range Not Satisfiable"),
        _ => None,
    }
}

/// Applies Go's reason phrase to a response whose status text differs.
pub(crate) fn apply_go_reason_phrase(response: &mut Response) {
    if response.extensions().get::<ReasonPhrase>().is_some() {
        return;
    }
    if let Some(text) = go_reason_phrase(response.status())
        && let Ok(phrase) = ReasonPhrase::try_from(text.as_bytes())
    {
        response.extensions_mut().insert(phrase);
    }
}

fn with_reason(mut response: Response, reason: &str) -> Response {
    if let Ok(phrase) = ReasonPhrase::try_from(reason.as_bytes()) {
        response.extensions_mut().insert(phrase);
    }
    response
}

/// Go writes these connection-level errors itself: plain text, no length,
/// `Connection: close`.
fn conn_error(status: StatusCode, reason: &str, body: &str) -> Response {
    // hyper frames a streamed HTTP/1.1 body as chunked, so the front door
    // writes the reply itself (see `close_delimited_wire`); the body here is
    // only what a unit test without a socket sees.
    let mut response = Response::new(Body::from_stream(OnceStream(Some(
        axum::body::Bytes::from(body.to_owned()),
    ))));
    response
        .extensions_mut()
        .insert(CloseDelimited(body.to_owned()));
    *response.status_mut() = status;
    response.headers_mut().insert(
        header::CONTENT_TYPE,
        HeaderValue::from_static("text/plain; charset=utf-8"),
    );
    response
        .headers_mut()
        .insert(header::CONNECTION, HeaderValue::from_static("close"));
    with_reason(response, reason)
}

/// Marks a reply Go writes with no length and `Connection: close`.
#[derive(Clone)]
struct CloseDelimited(String);

/// The exact bytes Go's `http.Server` writes for a [`CloseDelimited`] reply:
/// status line, `Content-Type`, `Connection: close`, no `Date`, no length.
pub(crate) fn close_delimited_wire(response: &Response) -> Option<Vec<u8>> {
    let body = response.extensions().get::<CloseDelimited>()?;
    let reason = response.extensions().get::<ReasonPhrase>().map_or_else(
        || {
            response
                .status()
                .canonical_reason()
                .unwrap_or("")
                .to_owned()
        },
        |phrase| String::from_utf8_lossy(phrase.as_bytes()).into_owned(),
    );
    let mut wire = format!(
        "HTTP/1.1 {} {reason}\r\nContent-Type: text/plain; charset=utf-8\r\nConnection: close\r\n\r\n",
        response.status().as_u16()
    )
    .into_bytes();
    wire.extend_from_slice(body.0.as_bytes());
    Some(wire)
}

struct OnceStream(Option<axum::body::Bytes>);

impl futures_core::Stream for OnceStream {
    type Item = Result<axum::body::Bytes, std::io::Error>;

    fn poll_next(
        mut self: std::pin::Pin<&mut Self>,
        _context: &mut std::task::Context<'_>,
    ) -> std::task::Poll<Option<Self::Item>> {
        std::task::Poll::Ready(self.0.take().map(Ok))
    }
}

fn bad_request(detail: Option<&str>) -> Response {
    match detail {
        Some(detail) => conn_error(
            StatusCode::BAD_REQUEST,
            &format!("Bad Request: {detail}"),
            &format!("400 Bad Request: {detail}"),
        ),
        None => conn_error(StatusCode::BAD_REQUEST, "Bad Request", "400 Bad Request"),
    }
}

/// `httpguts.ValidHostHeader`: every byte must be one of
/// `!$%&'()*+,-.0-9:;=A-Z[]_a-z~`.
pub(crate) fn valid_host_header(value: &[u8]) -> bool {
    value.iter().all(|byte| {
        byte.is_ascii_alphanumeric()
            || matches!(
                byte,
                b'!' | b'$'
                    | b'%'
                    | b'&'
                    | b'\''
                    | b'('
                    | b')'
                    | b'*'
                    | b'+'
                    | b','
                    | b'-'
                    | b'.'
                    | b':'
                    | b';'
                    | b'='
                    | b'['
                    | b']'
                    | b'_'
                    | b'~'
            )
    })
}

/// Size Go charges against `MaxHeaderBytes` for a parsed request: the request
/// line, every header line and the terminating blank line.
fn header_block_bytes(parts: &Parts) -> usize {
    let target = parts
        .uri
        .path_and_query()
        .map_or(parts.uri.path().len(), |value| value.as_str().len());
    let line = parts.method.as_str().len() + 1 + target + " HTTP/1.1\r\n".len();
    let headers: usize = parts
        .headers
        .iter()
        .map(|(name, value)| name.as_str().len() + 2 + value.as_bytes().len() + 2)
        .sum();
    line + headers + 2
}

/// Checks `http.Server` applies to a request before routing it. Returns the
/// response Go would have written, or `None` to continue.
pub(crate) fn connection_precheck(parts: &Parts) -> Option<Response> {
    if header_block_bytes(parts) > MAX_HEADER_BLOCK_BYTES {
        return Some(conn_error(
            StatusCode::REQUEST_HEADER_FIELDS_TOO_LARGE,
            "Request Header Fields Too Large",
            "431 Request Header Fields Too Large",
        ));
    }
    // `url.ParseRequestURI`: control characters and malformed escapes.
    let target_path = parts.uri.path();
    if target_path.bytes().any(|byte| byte < 0x20 || byte == 0x7f)
        || unescape(target_path.as_bytes(), Unescape::Path).is_none()
    {
        return Some(bad_request(None));
    }
    let hosts: Vec<&HeaderValue> = parts.headers.get_all(header::HOST).iter().collect();
    if parts.version >= Version::HTTP_11 && hosts.is_empty() && parts.method != Method::CONNECT {
        return Some(bad_request(Some("missing required Host header")));
    }
    if hosts.len() > 1 {
        return Some(bad_request(None));
    }
    if hosts.len() == 1 && !valid_host_header(hosts[0].as_bytes()) {
        return Some(bad_request(Some("malformed Host header")));
    }
    let codings: Vec<String> = parts
        .headers
        .get_all(header::TRANSFER_ENCODING)
        .iter()
        .flat_map(|value| {
            String::from_utf8_lossy(value.as_bytes())
                .split(',')
                .map(|item| item.trim().to_ascii_lowercase())
                .collect::<Vec<_>>()
        })
        .collect();
    if !codings.is_empty() && codings != ["chunked"] {
        return Some(conn_error(
            StatusCode::NOT_IMPLEMENTED,
            "Not Implemented",
            "Unsupported transfer encoding",
        ));
    }
    let expect = parts.headers.get(header::EXPECT);
    if let Some(expect) = expect
        && !expect.as_bytes().eq_ignore_ascii_case(b"100-continue")
    {
        let mut response = Response::new(Body::empty());
        *response.status_mut() = StatusCode::EXPECTATION_FAILED;
        response
            .headers_mut()
            .insert(header::CONNECTION, HeaderValue::from_static("close"));
        response
            .headers_mut()
            .insert(header::CONTENT_LENGTH, HeaderValue::from_static("0"));
        return Some(response);
    }
    None
}

// ---------------------------------------------------------------------------
// URL escaping (net/url)

#[derive(Clone, Copy, PartialEq, Eq)]
enum Unescape {
    Path,
    Query,
}

fn unhex(byte: u8) -> Option<u8> {
    match byte {
        b'0'..=b'9' => Some(byte - b'0'),
        b'a'..=b'f' => Some(byte - b'a' + 10),
        b'A'..=b'F' => Some(byte - b'A' + 10),
        _ => None,
    }
}

/// `url.unescape`: `None` for a malformed escape.
fn unescape(value: &[u8], mode: Unescape) -> Option<Vec<u8>> {
    let mut out = Vec::with_capacity(value.len());
    let mut index = 0;
    while index < value.len() {
        match value[index] {
            b'%' => {
                let high = value.get(index + 1).copied().and_then(unhex)?;
                let low = value.get(index + 2).copied().and_then(unhex)?;
                out.push(high << 4 | low);
                index += 3;
            }
            b'+' if mode == Unescape::Query => {
                out.push(b' ');
                index += 1;
            }
            byte => {
                out.push(byte);
                index += 1;
            }
        }
    }
    Some(out)
}

/// `url.PathEscape`-style escaping of a whole path (`encodePath` mode), as
/// used by `URL.String()` for a `Path`-only URL.
fn escape_path(value: &[u8]) -> String {
    let mut out = String::with_capacity(value.len());
    for &byte in value {
        let literal = byte.is_ascii_alphanumeric()
            || matches!(
                byte,
                b'-' | b'_'
                    | b'.'
                    | b'~'
                    | b'/'
                    | b'$'
                    | b'&'
                    | b'+'
                    | b','
                    | b':'
                    | b';'
                    | b'='
                    | b'@'
            );
        if literal {
            out.push(char::from(byte));
        } else {
            out.push_str(&format!("%{byte:02X}"));
        }
    }
    out
}

/// `url.Values` as produced by `URL.Query()`: malformed pairs are dropped.
#[derive(Debug, Default, Clone)]
pub(crate) struct GoQuery(Vec<(Vec<u8>, Vec<u8>)>);

impl GoQuery {
    pub(crate) fn parse(query: &str) -> Self {
        let mut pairs = Vec::new();
        for part in query.split('&') {
            if part.is_empty() || part.contains(';') {
                continue;
            }
            let (key, value) = part.split_once('=').unwrap_or((part, ""));
            let (Some(key), Some(value)) = (
                unescape(key.as_bytes(), Unescape::Query),
                unescape(value.as_bytes(), Unescape::Query),
            ) else {
                continue;
            };
            pairs.push((key, value));
        }
        Self(pairs)
    }

    pub(crate) fn has(&self, key: &str) -> bool {
        self.0.iter().any(|(name, _)| name == key.as_bytes())
    }

    /// First value of `key`, as `Values.Get` returns it.
    pub(crate) fn get_bytes(&self, key: &str) -> Option<&[u8]> {
        self.0
            .iter()
            .find(|(name, _)| name == key.as_bytes())
            .map(|(_, value)| value.as_slice())
    }

    /// First value of `key`; invalid UTF-8 becomes `None` (such a value can
    /// never be a valid vault path or job id).
    pub(crate) fn get(&self, key: &str) -> Option<String> {
        self.get_bytes(key)
            .and_then(|value| String::from_utf8(value.to_vec()).ok())
    }

    /// Like [`Self::get`], but an absent or non-UTF-8 value is the empty
    /// string, matching `Values.Get`'s "missing is empty" contract.
    pub(crate) fn get_or_empty(&self, key: &str) -> String {
        self.get(key).unwrap_or_default()
    }
}

impl<S: Send + Sync> FromRequestParts<S> for GoQuery {
    type Rejection = std::convert::Infallible;

    async fn from_request_parts(parts: &mut Parts, _state: &S) -> Result<Self, Self::Rejection> {
        Ok(Self::parse(parts.uri.query().unwrap_or_default()))
    }
}

// ---------------------------------------------------------------------------
// ServeMux

/// `path.Clean` for a rooted path.
fn path_clean_rooted(path: &str) -> String {
    let mut stack: Vec<&str> = Vec::new();
    for segment in path.split('/') {
        match segment {
            "" | "." => {}
            ".." => {
                stack.pop();
            }
            other => stack.push(other),
        }
    }
    format!("/{}", stack.join("/"))
}

/// `ServeMux.cleanPath`: `path.Clean` that keeps a trailing slash.
pub(crate) fn clean_path(path: &str) -> String {
    if path.is_empty() {
        return "/".to_owned();
    }
    let rooted = if path.starts_with('/') {
        path.to_owned()
    } else {
        format!("/{path}")
    };
    let cleaned = path_clean_rooted(&rooted);
    if rooted.ends_with('/') && cleaned != "/" {
        if rooted.len() == cleaned.len() + 1 && rooted.starts_with(&cleaned) {
            return rooted;
        }
        return format!("{cleaned}/");
    }
    cleaned
}

#[derive(Clone, Copy)]
enum Segment {
    Literal(&'static str),
    Wildcard,
}

struct Pattern {
    methods: &'static [&'static str],
    segments: &'static [Segment],
}

use Segment::{Literal as L, Wildcard as W};

/// The 21 patterns registered by `Server.routes`. A `GET` pattern also
/// matches `HEAD`.
const PATTERNS: &[Pattern] = &[
    Pattern {
        methods: &["GET", "HEAD"],
        segments: &[L("healthz")],
    },
    Pattern {
        methods: &["GET", "HEAD"],
        segments: &[L("api"), L("v1"), L("status")],
    },
    Pattern {
        methods: &["GET", "HEAD"],
        segments: &[L("api"), L("v1"), L("snapshot")],
    },
    Pattern {
        methods: &["GET", "HEAD"],
        segments: &[L("api"), L("v1"), L("files")],
    },
    Pattern {
        methods: &["PUT"],
        segments: &[L("api"), L("v1"), L("files")],
    },
    Pattern {
        methods: &["POST"],
        segments: &[L("api"), L("v1"), L("ingest")],
    },
    Pattern {
        methods: &["GET", "HEAD"],
        segments: &[L("api"), L("v1"), L("jobs")],
    },
    Pattern {
        methods: &["POST"],
        segments: &[L("api"), L("v1"), L("jobs"), L("retry")],
    },
    Pattern {
        methods: &["POST"],
        segments: &[L("api"), L("v1"), L("command")],
    },
    Pattern {
        methods: &["POST"],
        segments: &[L("api"), L("v1"), L("ai"), L("ask")],
    },
    Pattern {
        methods: &["POST"],
        segments: &[L("api"), L("v1"), L("ai"), L("transform")],
    },
    Pattern {
        methods: &["GET", "HEAD"],
        segments: &[L("api"), L("v1"), L("notebooks")],
    },
    Pattern {
        methods: &["GET", "HEAD"],
        segments: &[L("api"), L("v1"), L("notebooks"), W],
    },
    Pattern {
        methods: &["POST"],
        segments: &[L("api"), L("v1"), L("worker"), L("lease")],
    },
    Pattern {
        methods: &["GET", "HEAD"],
        segments: &[L("api"), L("v1"), L("worker"), L("input")],
    },
    Pattern {
        methods: &["POST"],
        segments: &[L("api"), L("v1"), L("worker"), L("complete")],
    },
    Pattern {
        methods: &["POST"],
        segments: &[L("api"), L("v1"), L("worker"), L("fail")],
    },
    Pattern {
        methods: &["POST"],
        segments: &[L("api"), L("v1"), L("share")],
    },
    Pattern {
        methods: &["GET", "HEAD"],
        segments: &[L("api"), L("v1"), L("shares")],
    },
    Pattern {
        methods: &["DELETE"],
        segments: &[L("api"), L("v1"), L("share"), W],
    },
    Pattern {
        methods: &["GET", "HEAD"],
        segments: &[L("s"), W],
    },
];

/// Result of matching a cleaned, escaped request path against the table.
#[derive(Debug, PartialEq, Eq)]
pub(crate) enum Matched {
    /// A pattern matches; the path rewritten so each literal segment is its
    /// canonical spelling and each wildcard keeps its escaped spelling.
    Route(String),
    /// Some pattern matches the path under another method.
    MethodNotAllowed(Vec<&'static str>),
    NotFound,
}

pub(crate) fn match_route(method: &str, escaped_path: &str) -> Matched {
    let Some(rest) = escaped_path.strip_prefix('/') else {
        return Matched::NotFound;
    };
    let escaped_segments: Vec<&str> = rest.split('/').collect();
    let Some(unescaped): Option<Vec<Vec<u8>>> = escaped_segments
        .iter()
        .map(|segment| unescape(segment.as_bytes(), Unescape::Path))
        .collect()
    else {
        return Matched::NotFound;
    };
    let mut allowed: Vec<&'static str> = Vec::new();
    let mut route = None;
    for pattern in PATTERNS {
        if pattern.segments.len() != unescaped.len() {
            continue;
        }
        let matches =
            pattern
                .segments
                .iter()
                .zip(&unescaped)
                .all(|(segment, value)| match segment {
                    Segment::Literal(literal) => value == literal.as_bytes(),
                    Segment::Wildcard => !value.is_empty(),
                });
        if !matches {
            continue;
        }
        for allowed_method in pattern.methods {
            if !allowed.contains(allowed_method) {
                allowed.push(allowed_method);
            }
        }
        if pattern.methods.contains(&method) && route.is_none() {
            let canonical: Vec<&str> = pattern
                .segments
                .iter()
                .zip(&escaped_segments)
                .map(|(segment, escaped)| match segment {
                    Segment::Literal(literal) => *literal,
                    Segment::Wildcard => *escaped,
                })
                .collect();
            route = Some(format!("/{}", canonical.join("/")));
        }
    }
    if let Some(route) = route {
        return Matched::Route(route);
    }
    if allowed.is_empty() {
        return Matched::NotFound;
    }
    allowed.sort_unstable();
    Matched::MethodNotAllowed(allowed)
}

fn html_escape(value: &str) -> String {
    value
        .replace('&', "&amp;")
        .replace('<', "&lt;")
        .replace('>', "&gt;")
        .replace('"', "&#34;")
        .replace('\'', "&#39;")
}

/// `http.Redirect(w, r, location, 307)`.
fn redirect(method: &Method, location: &str) -> Response {
    let head = *method == Method::GET || *method == Method::HEAD;
    let body = if *method == Method::GET {
        format!(
            "<a href=\"{}\">Temporary Redirect</a>.\n\n",
            html_escape(location)
        )
    } else {
        String::new()
    };
    let mut response = Response::new(Body::from(body));
    *response.status_mut() = StatusCode::TEMPORARY_REDIRECT;
    if let Ok(value) = HeaderValue::from_str(location) {
        response.headers_mut().insert(header::LOCATION, value);
    }
    if head {
        response.headers_mut().insert(
            header::CONTENT_TYPE,
            HeaderValue::from_static("text/html; charset=utf-8"),
        );
    }
    response
}

fn plain(status: StatusCode, body: &'static str) -> Response {
    let mut response = Response::new(Body::from(body));
    *response.status_mut() = status;
    response.headers_mut().insert(
        header::CONTENT_TYPE,
        HeaderValue::from_static("text/plain; charset=utf-8"),
    );
    response
}

pub(crate) fn not_found() -> Response {
    plain(StatusCode::NOT_FOUND, "404 page not found\n")
}

fn method_not_allowed(allowed: &[&str]) -> Response {
    let mut response = plain(StatusCode::METHOD_NOT_ALLOWED, "Method Not Allowed\n");
    if let Ok(value) = HeaderValue::from_str(&allowed.join(", ")) {
        response.headers_mut().insert(header::ALLOW, value);
    }
    response
}

/// `ServeMux.ServeHTTP` for the adapter's route table. Responds for
/// redirects, malformed escapes, `*` targets, 404 and 405, and otherwise
/// forwards the request with a canonical path.
pub(crate) async fn serve_mux(mut request: Request, next: Next) -> Response {
    let target = request.uri().to_string();
    if target == "*" {
        return if request.method() == Method::OPTIONS {
            let mut response = Response::new(Body::empty());
            response
                .headers_mut()
                .insert(header::CONTENT_LENGTH, HeaderValue::from_static("0"));
            response
        } else {
            let mut response = Response::new(Body::empty());
            *response.status_mut() = StatusCode::BAD_REQUEST;
            response
                .headers_mut()
                .insert(header::CONNECTION, HeaderValue::from_static("close"));
            response
        };
    }
    let raw_path = request.uri().path().to_owned();
    // CONNECT requests are matched as sent; every other method is cleaned.
    let cleaned = if request.method() == Method::CONNECT {
        raw_path.clone()
    } else {
        clean_path(&raw_path)
    };
    if cleaned != raw_path {
        let mut location = escape_path(cleaned.as_bytes());
        if let Some(query) = request.uri().query()
            && !query.is_empty()
        {
            location.push('?');
            location.push_str(query);
        }
        return redirect(request.method(), &location);
    }
    match match_route(request.method().as_str(), &cleaned) {
        Matched::NotFound => not_found(),
        Matched::MethodNotAllowed(allowed) => method_not_allowed(&allowed),
        Matched::Route(canonical) => {
            if canonical != raw_path {
                let mut path_and_query = canonical;
                if let Some(query) = request.uri().query() {
                    path_and_query.push('?');
                    path_and_query.push_str(query);
                }
                let mut parts = request.uri().clone().into_parts();
                if let Ok(value) = path_and_query.parse() {
                    parts.path_and_query = Some(value);
                    if let Ok(uri) = axum::http::Uri::from_parts(parts) {
                        *request.uri_mut() = uri;
                    }
                }
            }
            next.run(request).await
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn clean_path_matches_go() {
        for (input, expected) in [
            ("/healthz", "/healthz"),
            ("//healthz", "/healthz"),
            ("/./healthz", "/healthz"),
            ("/api/../healthz", "/healthz"),
            ("/api/v1/status/", "/api/v1/status/"),
            ("/api//v1/", "/api/v1/"),
            ("/a/b/..", "/a"),
            ("/a/b/../", "/a/"),
            ("/../..", "/"),
            ("/", "/"),
            ("", "/"),
            ("a", "/a"),
        ] {
            assert_eq!(clean_path(input), expected, "clean {input:?}");
        }
    }

    #[test]
    fn query_matches_go_values() {
        let query = GoQuery::parse("path=Hello.md;x=1&a=1&path=2&b=%zz&c=%41+b&&=v");
        assert_eq!(query.get("path"), Some("2".to_owned()));
        assert!(!query.has("b"));
        assert_eq!(query.get("c"), Some("A b".to_owned()));
        assert_eq!(query.get("a"), Some("1".to_owned()));
        assert!(query.has(""));
        let query = GoQuery::parse("path=a&path=b");
        assert_eq!(query.get("path"), Some("a".to_owned()));
    }

    #[test]
    fn mux_matches_decoded_segments_and_reports_allow() {
        assert_eq!(
            match_route("GET", "/%61pi/v1/status"),
            Matched::Route("/api/v1/status".to_owned())
        );
        assert_eq!(
            match_route("HEAD", "/api/v1/files"),
            Matched::Route("/api/v1/files".to_owned())
        );
        assert_eq!(
            match_route("POST", "/api/v1/files"),
            Matched::MethodNotAllowed(vec!["GET", "HEAD", "PUT"])
        );
        assert_eq!(match_route("GET", "/api/v1/notebooks/"), Matched::NotFound);
        assert_eq!(
            match_route("GET", "/api/v1/notebooks/a%2Fb"),
            Matched::Route("/api/v1/notebooks/a%2Fb".to_owned())
        );
        assert_eq!(
            match_route("PATCH", "/api/v1/share/x"),
            Matched::MethodNotAllowed(vec!["DELETE"])
        );
        assert_eq!(match_route("GET", "/nope"), Matched::NotFound);
    }

    #[test]
    fn host_validity_matches_httpguts() {
        for ok in ["x", "127.0.0.1:80", "[::1]:80", "a_b~c", "a=b"] {
            assert!(valid_host_header(ok.as_bytes()), "{ok}");
        }
        for bad in ["a b", "user@host", "a/b", "a<b", "a?b", "a#b"] {
            assert!(!valid_host_header(bad.as_bytes()), "{bad}");
        }
    }

    #[test]
    fn redirect_location_reescapes_percent() {
        assert_eq!(escape_path(b"/a%20b/%7e"), "/a%2520b/%257e");
    }
}
