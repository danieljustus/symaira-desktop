use std::{error::Error, fmt, net::IpAddr, time::Duration};

use http_body_util::BodyExt;
use hyper::{Method, Request, Uri, body::Incoming, header};
use hyper_util::{
    client::legacy::{Client as HttpClient, connect::HttpConnector},
    rt::TokioExecutor,
};
use serde::{
    Deserialize, Deserializer,
    de::{DeserializeSeed, IgnoredAny, MapAccess, Visitor},
};
use serde_json::json;
use tokio::time::timeout;

use crate::local_ollama_endpoint_for_path;

const DEFAULT_QUERY_EXPANSION_TIMEOUT: Duration = Duration::from_secs(120);
const MAX_QUERY_EXPANSION_RESPONSE_BYTES: usize = 4 << 20;
const MAX_PROVIDER_ERROR_BYTES: usize = 512;

#[derive(Debug)]
pub enum LocalQueryExpansionError {
    InvalidEndpoint,
    Transport(String),
    Timeout,
    HttpStatus { status: u16, body: String },
    ResponseTooLarge,
    InvalidResponse(String),
}

impl fmt::Display for LocalQueryExpansionError {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Self::InvalidEndpoint => formatter.write_str(
                "ollama: invalid_endpoint: query expansion endpoint must use cleartext HTTP on a loopback host",
            ),
            Self::Transport(message) => write!(formatter, "ollama chat request failed: {message}"),
            Self::Timeout => formatter.write_str("ollama chat request failed: context deadline exceeded"),
            Self::HttpStatus { status, body } if body.is_empty() => {
                write!(formatter, "ollama returned HTTP {status}: ")
            }
            Self::HttpStatus { status, body } => {
                write!(formatter, "ollama returned HTTP {status}: {body}")
            }
            Self::ResponseTooLarge => formatter
                .write_str("failed to decode ollama response: response exceeds 4 MiB limit"),
            Self::InvalidResponse(message) => {
                write!(formatter, "{message}")
            }
        }
    }
}

impl Error for LocalQueryExpansionError {}

/// Sends the existing Go HyDE prompt to Ollama's non-streaming `/api/chat`
/// endpoint. The configured path is rewritten the same way as the Go caller;
/// the endpoint remains confined to the existing local Ollama boundary.
pub async fn expand_local_ollama_query(
    configured_url: &str,
    model: &str,
    query: &str,
    request_timeout: Duration,
) -> Result<String, LocalQueryExpansionError> {
    let endpoint = local_query_expansion_endpoint(configured_url)
        .ok_or(LocalQueryExpansionError::InvalidEndpoint)?;
    let prompt = format!(
        "Write a short, factual passage (2-4 sentences) that directly answers or describes the following topic. This passage will be used to improve document search relevance.\n\nTopic: {query}"
    );
    let body = serde_json::to_vec(&json!({
        "model": model,
        "messages": [{"role": "user", "content": prompt}],
        "stream": false,
    }))
    .map_err(|error| {
        LocalQueryExpansionError::InvalidResponse(format!("encode request: {error}"))
    })?;
    let request = Request::builder()
        .method(Method::POST)
        .uri(endpoint)
        .header(header::CONTENT_TYPE, "application/json")
        .body(http_body_util::Full::new(hyper::body::Bytes::from(body)))
        .map_err(|error| {
            LocalQueryExpansionError::InvalidResponse(format!("build request: {error}"))
        })?;
    let mut connector = HttpConnector::new();
    connector.enforce_http(true);
    let client: HttpClient<HttpConnector, _> =
        HttpClient::builder(TokioExecutor::new()).build(connector);
    let request_timeout = if request_timeout.is_zero() {
        DEFAULT_QUERY_EXPANSION_TIMEOUT
    } else {
        request_timeout
    };
    timeout(request_timeout, async move {
        let response = client
            .request(request)
            .await
            .map_err(|error| LocalQueryExpansionError::Transport(error.to_string()))?;
        parse_chat_response(response).await
    })
    .await
    .unwrap_or(Err(LocalQueryExpansionError::Timeout))
}

fn local_query_expansion_endpoint(configured_url: &str) -> Option<Uri> {
    let chat_url = configured_url
        .replacen("/api/embeddings", "/api/chat", 1)
        .replacen("/api/embed", "/api/chat", 1);
    let parsed = chat_url.parse::<Uri>().ok()?;
    let authority = parsed.authority()?;
    if authority.as_str().contains('@') || parsed.scheme_str()? != "http" {
        return None;
    }
    let host = parsed.host()?;
    let endpoint_path = parsed.path_and_query()?.as_str();
    if host.eq_ignore_ascii_case("localhost") {
        let port = parsed
            .port_u16()
            .map(|port| format!(":{port}"))
            .unwrap_or_default();
        let normalized = format!("http://127.0.0.1{port}");
        return local_ollama_endpoint_for_path(&normalized, endpoint_path);
    }
    if !host
        .trim_start_matches('[')
        .trim_end_matches(']')
        .parse::<IpAddr>()
        .is_ok_and(|address| address.is_loopback())
    {
        return None;
    }
    local_ollama_endpoint_for_path(&chat_url, endpoint_path)
}

async fn parse_chat_response(
    response: hyper::Response<Incoming>,
) -> Result<String, LocalQueryExpansionError> {
    let status = response.status().as_u16();
    if status != 200 {
        let body = read_body_prefix(response.into_body(), MAX_PROVIDER_ERROR_BYTES).await;
        let body = String::from_utf8_lossy(&body).trim().to_owned();
        return Err(LocalQueryExpansionError::HttpStatus { status, body });
    }
    let body = read_bounded_body(response.into_body(), MAX_QUERY_EXPANSION_RESPONSE_BYTES).await?;
    let mut deserializer = serde_json::Deserializer::from_slice(&body);
    let response = ChatResponse::deserialize(&mut deserializer).map_err(|error| {
        LocalQueryExpansionError::InvalidResponse(format!(
            "failed to decode ollama response: {error}"
        ))
    })?;
    Ok(response.message.content)
}

#[derive(Default)]
struct ChatResponse {
    message: ChatMessage,
}

#[derive(Default)]
struct ChatMessage {
    content: String,
}

impl<'de> Deserialize<'de> for ChatResponse {
    fn deserialize<D>(deserializer: D) -> Result<Self, D::Error>
    where
        D: Deserializer<'de>,
    {
        struct ResponseVisitor;

        impl<'de> Visitor<'de> for ResponseVisitor {
            type Value = ChatResponse;

            fn expecting(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
                formatter.write_str("an Ollama chat response object or null")
            }

            fn visit_unit<E>(self) -> Result<Self::Value, E>
            where
                E: serde::de::Error,
            {
                Ok(ChatResponse::default())
            }

            fn visit_map<M>(self, mut map: M) -> Result<Self::Value, M::Error>
            where
                M: MapAccess<'de>,
            {
                let mut message = ChatMessage::default();
                while let Some(key) = map.next_key::<String>()? {
                    if go_json_field_matches(&key, "message") {
                        // Go unmarshals duplicate struct-valued keys into the
                        // existing struct, so nested fields merge in input order.
                        message = map.next_value_seed(ChatMessageSeed(message))?;
                    } else {
                        map.next_value::<IgnoredAny>()?;
                    }
                }
                Ok(ChatResponse { message })
            }
        }

        deserializer.deserialize_any(ResponseVisitor)
    }
}

struct ChatMessageSeed(ChatMessage);

impl<'de> DeserializeSeed<'de> for ChatMessageSeed {
    type Value = ChatMessage;

    fn deserialize<D>(self, deserializer: D) -> Result<Self::Value, D::Error>
    where
        D: Deserializer<'de>,
    {
        struct MessageVisitor(ChatMessage);

        impl<'de> Visitor<'de> for MessageVisitor {
            type Value = ChatMessage;

            fn expecting(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
                formatter.write_str("an Ollama message object or null")
            }

            fn visit_unit<E>(self) -> Result<Self::Value, E>
            where
                E: serde::de::Error,
            {
                Ok(self.0)
            }

            fn visit_map<M>(self, mut map: M) -> Result<Self::Value, M::Error>
            where
                M: MapAccess<'de>,
            {
                let mut message = self.0;
                while let Some(key) = map.next_key::<String>()? {
                    if go_json_field_matches(&key, "content") {
                        // Go's string field keeps its previous value for null.
                        if let Some(content) = map.next_value::<Option<String>>()? {
                            message.content = content;
                        }
                    } else {
                        map.next_value::<IgnoredAny>()?;
                    }
                }
                Ok(message)
            }
        }

        deserializer.deserialize_any(MessageVisitor(self.0))
    }
}

fn go_json_field_matches(actual: &str, expected: &str) -> bool {
    actual.chars().count() == expected.len()
        && actual
            .chars()
            .zip(expected.chars())
            .all(|(actual, expected)| {
                actual.eq_ignore_ascii_case(&expected)
                    || (actual == 'ſ' && expected.eq_ignore_ascii_case(&'s'))
            })
}

async fn read_bounded_body(
    mut body: Incoming,
    limit: usize,
) -> Result<Vec<u8>, LocalQueryExpansionError> {
    let mut collected = Vec::new();
    while let Some(frame) = body.frame().await {
        let frame =
            frame.map_err(|error| LocalQueryExpansionError::Transport(error.to_string()))?;
        if let Ok(data) = frame.into_data() {
            if collected.len().saturating_add(data.len()) > limit {
                return Err(LocalQueryExpansionError::ResponseTooLarge);
            }
            collected.extend_from_slice(&data);
        }
    }
    Ok(collected)
}

async fn read_body_prefix(mut body: Incoming, limit: usize) -> Vec<u8> {
    let mut collected = Vec::with_capacity(limit);
    while collected.len() < limit {
        let Some(frame) = body.frame().await else {
            break;
        };
        let Ok(frame) = frame else {
            break;
        };
        let Ok(data) = frame.into_data() else {
            continue;
        };
        let remaining = limit - collected.len();
        collected.extend_from_slice(&data[..data.len().min(remaining)]);
    }
    collected
}

#[cfg(test)]
mod tests {
    use serde::Deserialize;

    use super::local_query_expansion_endpoint;

    #[test]
    fn chat_endpoint_reuses_local_ollama_boundary_and_go_path_rewrite() {
        assert_eq!(
            local_query_expansion_endpoint("http://localhost:11434/api/embeddings")
                .map(|uri| uri.to_string())
                .as_deref(),
            Some("http://127.0.0.1:11434/api/chat")
        );
        assert_eq!(
            local_query_expansion_endpoint("http://[::1]:11434/api/embed?mode=test")
                .map(|uri| uri.to_string())
                .as_deref(),
            Some("http://[::1]:11434/api/chat?mode=test")
        );
        assert_eq!(
            local_query_expansion_endpoint(
                "http://127.0.0.1/api/embeddings/api/embeddings/api/embeddings"
            )
            .map(|uri| uri.to_string())
            .as_deref(),
            Some("http://127.0.0.1/api/chat/api/chatdings/api/embeddings")
        );
        for rejected in [
            "https://127.0.0.1:11434/api/embeddings",
            "http://user:secret@127.0.0.1:11434/api/embeddings",
            "http://192.0.2.1:11434/api/embeddings",
        ] {
            assert!(
                local_query_expansion_endpoint(rejected).is_none(),
                "{rejected}"
            );
        }
    }

    #[test]
    fn chat_decoder_matches_go_case_duplicate_null_and_trailing_json_behavior() {
        let body = r#"{"meſſage":{"CONTENT":"first"},"message":{"content":null,"Content":"last"}} {"ignored":true}"#.as_bytes();
        let mut deserializer = serde_json::Deserializer::from_slice(body);
        let decoded =
            super::ChatResponse::deserialize(&mut deserializer).expect("decode first JSON value");
        assert_eq!(decoded.message.content, "last");

        let mut deserializer = serde_json::Deserializer::from_slice(
            br#"{"Message":{"Content":"kept"},"message":null}"#,
        );
        let decoded =
            super::ChatResponse::deserialize(&mut deserializer).expect("decode null duplicate");
        assert_eq!(decoded.message.content, "kept");
    }
}
