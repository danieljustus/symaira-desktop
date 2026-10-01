use std::{error::Error, fmt, time::Duration};

use http_body_util::BodyExt;
use hyper::{Method, Request, Uri, body::Incoming, header};
use hyper_util::{
    client::legacy::{Client as HttpClient, connect::HttpConnector},
    rt::TokioExecutor,
};
use serde::{
    Deserialize, Deserializer,
    de::{IgnoredAny, MapAccess, Visitor},
};
use serde_json::json;
use tokio::time::timeout;

use crate::local_ollama_endpoint_for_path;

const MAX_EMBEDDING_RESPONSE_BYTES: usize = 64 << 20;
const MAX_PROVIDER_ERROR_BYTES: usize = 8 << 10;
const MAX_PROVIDER_ERROR_EXCERPT_BYTES: usize = 512;
const DEFAULT_EMBEDDING_TIMEOUT: Duration = Duration::from_secs(120);

/// Failure returned by the one-request Ollama embedding transport.
///
/// Retry and hash-fallback policy belongs to the retrieval engine. In
/// particular, an HTTP or decode failure is never represented as an embedding.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum LocalEmbeddingError {
    EmptyInputs,
    InvalidEndpoint,
    Transport(String),
    Timeout,
    HttpStatus {
        status: u16,
        code: &'static str,
        body: String,
    },
    ResponseTooLarge,
    InvalidResponse(String),
    EmbeddingCount {
        expected: usize,
        actual: usize,
    },
}

impl fmt::Display for LocalEmbeddingError {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Self::EmptyInputs => {
                formatter.write_str("ollama: input_validation: embed inputs must not be empty")
            }
            Self::InvalidEndpoint => formatter.write_str(
                "ollama: invalid_endpoint: embedding endpoint must use cleartext HTTP on an IP-literal loopback host",
            ),
            Self::Transport(message) => write!(formatter, "ollama: transport_error: {message}"),
            Self::Timeout => {
                formatter.write_str("ollama: transport_error: context deadline exceeded")
            }
            Self::HttpStatus { status, body, .. } if body.is_empty() => {
                write!(
                    formatter,
                    "ollama: {} (status {status})",
                    self.contract_code()
                )
            }
            Self::HttpStatus { status, body, .. } => {
                write!(
                    formatter,
                    "ollama: {} (status {status}): {body}",
                    self.contract_code()
                )
            }
            Self::ResponseTooLarge => formatter
                .write_str("ollama: provider_error: embeddings response exceeds 64 MiB limit"),
            Self::InvalidResponse(message) => {
                write!(formatter, "ollama: provider_error: {message}")
            }
            Self::EmbeddingCount { expected, actual } => write!(
                formatter,
                "ollama: provider_error: expected {expected} embeddings, got {actual}"
            ),
        }
    }
}

impl Error for LocalEmbeddingError {}

impl LocalEmbeddingError {
    /// Returns the error category used by the Go `llmkit.Embed` contract.
    /// HTTP status categories follow llmkit's shared classifier; malformed
    /// successful responses remain provider errors.
    pub fn contract_code(&self) -> &'static str {
        match self {
            Self::EmptyInputs => "input_validation",
            Self::InvalidEndpoint => "invalid_endpoint",
            Self::Transport(_) | Self::Timeout => "transport_error",
            Self::HttpStatus { code, .. } => code,
            Self::ResponseTooLarge | Self::InvalidResponse(_) | Self::EmbeddingCount { .. } => {
                "provider_error"
            }
        }
    }

    /// Matches the Go retrieval wrapper's retry eligibility: transport errors
    /// and HTTP 5xx can be retried; model, input, decode, and other HTTP errors
    /// cannot. This method does not itself retry the request.
    pub fn is_transient(&self) -> bool {
        matches!(self, Self::Transport(_) | Self::Timeout)
            || matches!(self, Self::HttpStatus { status, .. } if *status >= 500)
    }
}

/// Builds the OpenAI-compatible embeddings endpoint while retaining the
/// existing local Ollama boundary: cleartext HTTP, numeric loopback IP only,
/// and no URL credentials. Configured path and query are discarded just as
/// Go's `ollamaBaseURL` does before appending `/v1/embeddings`.
pub fn local_ollama_embeddings_endpoint(configured_url: &str) -> Option<Uri> {
    let parsed = configured_url.parse::<Uri>().ok()?;
    let authority = parsed.authority()?;
    if authority.as_str().contains('@')
        || parsed.scheme_str()? != "http"
        || !parsed.host()?.eq_ignore_ascii_case("localhost")
    {
        return local_ollama_endpoint_for_path(configured_url, "/v1/embeddings");
    }
    // Go's Ollama client accepts the default localhost URL. Resolve only this
    // exact hostname to IPv4 loopback so the existing numeric-loopback policy
    // remains in force without DNS or changes to the chat endpoint boundary.
    // A provider bound exclusively to ::1 is outside this supported subset.
    let port = parsed
        .port_u16()
        .map(|port| format!(":{port}"))
        .unwrap_or_default();
    let normalized = format!("http://127.0.0.1{port}");
    local_ollama_endpoint_for_path(&normalized, "/v1/embeddings")
}

/// Sends one OpenAI-wire embedding request to a numeric loopback Ollama
/// endpoint. `dimensions` is omitted for the provider default, matching
/// `llmkit.Embed`'s `omitempty` request field. The retrieval engine owns
/// retries and fallback eligibility around this single transport attempt.
pub async fn embed_local_ollama(
    configured_url: &str,
    model: &str,
    inputs: &[String],
    dimensions: Option<usize>,
    request_timeout: Duration,
) -> Result<Vec<Vec<f32>>, LocalEmbeddingError> {
    if inputs.is_empty() {
        return Err(LocalEmbeddingError::EmptyInputs);
    }
    let endpoint = local_ollama_embeddings_endpoint(configured_url)
        .ok_or(LocalEmbeddingError::InvalidEndpoint)?;
    let request_timeout = if request_timeout.is_zero() {
        DEFAULT_EMBEDDING_TIMEOUT
    } else {
        request_timeout
    };
    let body = match dimensions.filter(|value| *value != 0) {
        Some(dimensions) => json!({"model": model, "input": inputs, "dimensions": dimensions}),
        None => json!({"model": model, "input": inputs}),
    };
    let request_body = serde_json::to_vec(&body).map_err(|error| {
        LocalEmbeddingError::InvalidResponse(format!("encode request: {error}"))
    })?;
    let request = Request::builder()
        .method(Method::POST)
        .uri(endpoint)
        .header(header::ACCEPT, "application/json")
        .header(header::CONTENT_TYPE, "application/json")
        .body(http_body_util::Full::new(hyper::body::Bytes::from(
            request_body,
        )))
        .map_err(|error| LocalEmbeddingError::InvalidResponse(format!("build request: {error}")))?;

    let mut connector = HttpConnector::new();
    connector.enforce_http(true);
    let client: HttpClient<HttpConnector, _> =
        HttpClient::builder(TokioExecutor::new()).build(connector);
    let result = timeout(request_timeout, async move {
        let response = client
            .request(request)
            .await
            .map_err(|error| LocalEmbeddingError::Transport(error.to_string()))?;
        parse_embedding_response(response, inputs.len()).await
    })
    .await;
    result.unwrap_or(Err(LocalEmbeddingError::Timeout))
}

async fn parse_embedding_response(
    response: hyper::Response<Incoming>,
    expected_count: usize,
) -> Result<Vec<Vec<f32>>, LocalEmbeddingError> {
    let status = response.status().as_u16();
    if status >= 300 {
        let body = read_body_prefix(response.into_body(), MAX_PROVIDER_ERROR_BYTES).await;
        let mut body = String::from_utf8_lossy(&body).trim().to_owned();
        let code = http_status_contract_code(status, &body);
        if body.len() > MAX_PROVIDER_ERROR_EXCERPT_BYTES {
            let mut end = MAX_PROVIDER_ERROR_EXCERPT_BYTES;
            while !body.is_char_boundary(end) {
                end -= 1;
            }
            body.truncate(end);
        }
        return Err(LocalEmbeddingError::HttpStatus { status, code, body });
    }
    let mut body = response.into_body();
    let mut bytes = Vec::new();
    while let Some(frame) = body.frame().await {
        let frame = frame.map_err(|error| LocalEmbeddingError::Transport(error.to_string()))?;
        let Ok(data) = frame.into_data() else {
            continue;
        };
        if bytes.len().saturating_add(data.len()) > MAX_EMBEDDING_RESPONSE_BYTES {
            return Err(LocalEmbeddingError::ResponseTooLarge);
        }
        bytes.extend_from_slice(&data);
    }
    let parsed: EmbeddingResponse = serde_json::from_slice(&bytes).map_err(|error| {
        LocalEmbeddingError::InvalidResponse(format!("decode embeddings response: {error}"))
    })?;
    if parsed.data.len() != expected_count {
        return Err(LocalEmbeddingError::EmbeddingCount {
            expected: expected_count,
            actual: parsed.data.len(),
        });
    }
    Ok(parsed.data.into_iter().map(|item| item.embedding).collect())
}

fn http_status_contract_code(status: u16, body: &str) -> &'static str {
    match status {
        401 | 403 => "auth_failure",
        429 => "rate_limited",
        404 => "model_not_found",
        400 => {
            let body = body.to_ascii_lowercase();
            if [
                "context_length_exceeded",
                "maximum context length",
                "context window",
                "too many tokens",
                "input length exceeds",
            ]
            .iter()
            .any(|marker| body.contains(marker))
            {
                "context_overflow"
            } else {
                "provider_error"
            }
        }
        _ => "provider_error",
    }
}

#[derive(Default)]
struct EmbeddingResponse {
    data: Vec<EmbeddingItem>,
}

struct EmbeddingItem {
    embedding: Vec<f32>,
}

impl<'de> Deserialize<'de> for EmbeddingResponse {
    fn deserialize<D>(deserializer: D) -> Result<Self, D::Error>
    where
        D: Deserializer<'de>,
    {
        struct ResponseVisitor;

        impl<'de> Visitor<'de> for ResponseVisitor {
            type Value = EmbeddingResponse;

            fn expecting(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
                formatter.write_str("an embeddings response object or null")
            }

            fn visit_unit<E>(self) -> Result<Self::Value, E>
            where
                E: serde::de::Error,
            {
                Ok(EmbeddingResponse::default())
            }

            fn visit_map<M>(self, mut map: M) -> Result<Self::Value, M::Error>
            where
                M: MapAccess<'de>,
            {
                let mut data = Vec::new();
                while let Some(key) = map.next_key::<String>()? {
                    if key.eq_ignore_ascii_case("data") {
                        // Go's encoding/json accepts case-insensitive field
                        // names and assigns duplicate matches in input order.
                        data = map
                            .next_value::<Option<Vec<EmbeddingItem>>>()?
                            .unwrap_or_default();
                    } else {
                        map.next_value::<IgnoredAny>()?;
                    }
                }
                Ok(EmbeddingResponse { data })
            }
        }

        deserializer.deserialize_any(ResponseVisitor)
    }
}

impl<'de> Deserialize<'de> for EmbeddingItem {
    fn deserialize<D>(deserializer: D) -> Result<Self, D::Error>
    where
        D: Deserializer<'de>,
    {
        struct ItemVisitor;

        impl<'de> Visitor<'de> for ItemVisitor {
            type Value = EmbeddingItem;

            fn expecting(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
                formatter.write_str("an embeddings data object")
            }

            fn visit_unit<E>(self) -> Result<Self::Value, E>
            where
                E: serde::de::Error,
            {
                Ok(EmbeddingItem {
                    embedding: Vec::new(),
                })
            }

            fn visit_map<M>(self, mut map: M) -> Result<Self::Value, M::Error>
            where
                M: MapAccess<'de>,
            {
                let mut embedding = Vec::new();
                while let Some(key) = map.next_key::<String>()? {
                    if key.eq_ignore_ascii_case("embedding") {
                        // Go treats missing and null slices as nil vectors.
                        embedding = map.next_value::<Option<Vec<f32>>>()?.unwrap_or_default();
                    } else {
                        map.next_value::<IgnoredAny>()?;
                    }
                }
                Ok(EmbeddingItem { embedding })
            }
        }

        deserializer.deserialize_any(ItemVisitor)
    }
}

async fn read_body_prefix(mut body: Incoming, limit: usize) -> Vec<u8> {
    let mut bytes = Vec::with_capacity(limit);
    while bytes.len() < limit {
        let Some(frame) = body.frame().await else {
            break;
        };
        let Ok(frame) = frame else {
            break;
        };
        let Ok(data) = frame.into_data() else {
            continue;
        };
        let remaining = limit - bytes.len();
        bytes.extend_from_slice(&data[..data.len().min(remaining)]);
    }
    bytes
}
