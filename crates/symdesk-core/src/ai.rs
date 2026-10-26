//! Shared Go `internal/ai` provider/config and one-shot streaming boundary.
//! Secret resolution is injected; this module never reads a keychain or ambient
//! API key. Sibling Hermes execution remains a separate, unported adapter.
use crate::config::Config;
use std::{
    cell::RefCell,
    collections::BTreeMap,
    io::{self, BufRead, BufReader, Cursor, Read},
    time::Duration,
};
use symaira_core_llm::{ChatOptions, ClientBuilder, GenerateOption, Message, lookup};
use ureq::AsSendBody;

pub const NOT_CONFIGURED: &str = "AI feature not configured";
const DEFAULT_MODEL: &str = "llama3.2";
const DEFAULT_ANTHROPIC_MODEL: &str = "claude-sonnet-5";

// Intentionally not Debug/Serialize: a resolved key must not enter diagnostics.
pub struct Provider {
    kind: &'static str,
    url: Option<String>,
    model: String,
    max_tokens: u32,
    api_key: String,
}
impl Provider {
    /// Resolve the Go dispatch precedence. Unknown ids fall back to Ollama;
    /// configured LLMModel applies only to Anthropic, not Ollama.
    pub fn resolve(
        config: &Config,
        environment: &BTreeMap<String, String>,
        mut resolve_key: impl FnMut(&str) -> String,
    ) -> Result<Self, String> {
        let env = |name: &str| {
            environment
                .get(name)
                .map(String::as_str)
                .unwrap_or_default()
        };
        if config.llm_provider == "hermes" {
            return Err("Rust AI provider \"hermes\" is not implemented".to_owned());
        }
        if config.llm_provider == "anthropic" {
            let key = resolve_key(config.api_key_reference());
            if key.is_empty() {
                return Err(NOT_CONFIGURED.to_owned());
            }
            return Ok(Self {
                kind: "anthropic",
                url: (!env("SYMDESK_ANTHROPIC_URL").is_empty())
                    .then(|| env("SYMDESK_ANTHROPIC_URL").to_owned()),
                model: if config.llm_model.is_empty() {
                    DEFAULT_ANTHROPIC_MODEL.to_owned()
                } else {
                    config.llm_model.clone()
                },
                max_tokens: if config.max_tokens <= 0 {
                    8192
                } else {
                    u32::try_from(config.max_tokens).unwrap_or(u32::MAX)
                },
                api_key: key,
            });
        }
        let environment_url = env("SYMDESK_OLLAMA_URL").trim_end_matches('/');
        let url = if environment_url.is_empty() {
            config.ollama_url.trim_end_matches('/')
        } else {
            environment_url
        };
        if url.is_empty() {
            return Err(NOT_CONFIGURED.to_owned());
        }
        let model = env("SYMDESK_OLLAMA_MODEL");
        Ok(Self {
            kind: "ollama",
            url: Some(ollama_root(url).to_owned()),
            model: if model.is_empty() {
                DEFAULT_MODEL.to_owned()
            } else {
                model.to_owned()
            },
            max_tokens: 0,
            api_key: String::new(),
        })
    }
    /// Stream through the pinned CoreKit transport. Callback errors stop the
    /// stream; callers own cancellation of their output consumer.
    pub fn stream(
        &self,
        prompt: &str,
        mut on_chunk: impl FnMut(&str) -> symaira_core_llm::Result<()>,
    ) -> Result<(), String> {
        let descriptor = lookup(self.kind)
            .ok_or_else(|| format!("{} descriptor missing from embedded registry", self.kind))?;
        let agent = ureq::Agent::config_builder()
            .http_status_as_error(false)
            .max_redirects(0)
            .timeout_global(Some(Duration::from_secs(5 * 60)))
            .middleware(go_request_json)
            .build()
            .into();
        let mut builder = ClientBuilder::new(descriptor.clone(), "")
            .api_key(&self.api_key)
            .agent(agent)
            .timeout(Duration::from_secs(5 * 60));
        if let Some(url) = &self.url {
            builder = builder.base_url(url);
        }
        let client = builder
            .build()
            .map_err(|error| self.redact(&error.to_string()))?;
        let result = if self.kind == "ollama" {
            client.generate(&self.model, prompt, &GenerateOption::default(), |chunk| {
                if chunk.response.is_empty() {
                    Ok(())
                } else {
                    on_chunk(&chunk.response)
                }
            })
        } else {
            let callback = RefCell::new(&mut on_chunk);
            let callback_error = RefCell::new(None);
            let result = client.stream_chat(
                &self.model,
                &[Message {
                    role: "user".to_owned(),
                    content: prompt.to_owned(),
                }],
                Some(&ChatOptions {
                    max_tokens: self.max_tokens,
                    ..ChatOptions::default()
                }),
                |delta| callback.borrow_mut()(delta),
                |reason| {
                    if reason == "max_tokens" {
                        *callback_error.borrow_mut() = callback.borrow_mut()(
                            "\n\n⚠️ **[Output truncated due to token limit]**",
                        )
                        .err();
                    }
                },
            );
            result.and_then(|()| callback_error.into_inner().map_or(Ok(()), Err))
        };
        result.map_err(|error| format!("{}: {}", self.kind, self.redact(&error.to_string())))
    }
    pub fn prompt_one(&self, prompt: &str) -> Result<String, String> {
        let mut result = String::new();
        self.stream(prompt, |chunk| {
            result.push_str(chunk);
            Ok(())
        })?;
        Ok(result.trim().to_owned())
    }
    fn redact(&self, message: &str) -> String {
        if self.api_key.is_empty() {
            message.to_owned()
        } else {
            message.replace(&self.api_key, "[REDACTED]")
        }
    }
}
/// Go PromptOne discards partial output on a failed stream and maps the
/// not-configured sentinel to the provider-specific one-shot diagnostic.
pub fn prompt_one(
    config: &Config,
    environment: &BTreeMap<String, String>,
    resolve_key: impl FnMut(&str) -> String,
    prompt: &str,
) -> Result<String, String> {
    let provider = Provider::resolve(config, environment, resolve_key).map_err(|error| {
        if error == NOT_CONFIGURED {
            if config.llm_provider == "anthropic" {
                "anthropic API key not resolved".to_owned()
            } else {
                "SYMDESK_OLLAMA_URL not set".to_owned()
            }
        } else {
            error
        }
    })?;
    provider.prompt_one(prompt)
}

fn ollama_root(raw: &str) -> &str {
    raw.match_indices('/')
        .find_map(|(index, _)| (index > "http://".len()).then_some(&raw[..index]))
        .unwrap_or(raw)
}

// CoreKit uses serde_json for request bytes. Go encoding/json additionally
// escapes HTML and Unicode line separators. Adapt only that wire boundary;
// retain CoreKit's request schema, status mapping and streaming parsers.
fn go_request_json(
    request: ureq::http::Request<ureq::SendBody>,
    next: ureq::middleware::MiddlewareNext,
) -> Result<ureq::http::Response<ureq::Body>, ureq::Error> {
    let (mut parts, body) = request.into_parts();
    let mut json = String::new();
    body.into_reader().read_to_string(&mut json)?;
    if parts.uri.path().ends_with("/messages") {
        // Go Anthropic's typed request uses declaration order, unlike the
        // CoreKit Rust Value map's sorted keys. This boundary sends only the
        // single-user-message schema owned by internal/ai/anthropic.go.
        let request: AnthropicRequest = serde_json::from_str(&json).map_err(io::Error::other)?;
        json = serde_json::to_string(&request).map_err(io::Error::other)?;
    }
    let encoded = json
        .replace('&', "\\u0026")
        .replace('<', "\\u003c")
        .replace('>', "\\u003e")
        .replace('\u{2028}', "\\u2028")
        .replace('\u{2029}', "\\u2029");
    parts.headers.remove(ureq::http::header::CONTENT_LENGTH);
    let native_generate = parts.uri.path() == "/api/generate";
    let response = next.handle(ureq::http::Request::from_parts(
        parts,
        encoded.as_bytes().as_body(),
    ))?;
    if !native_generate || !response.status().is_success() {
        return Ok(response);
    }
    let (parts, body) = response.into_parts();
    let body = ureq::Body::builder().reader(GoGenerateDefaults {
        input: BufReader::new(body.into_reader()),
        pending: Cursor::new(Vec::new()),
    });
    Ok(ureq::http::Response::from_parts(parts, body))
}

#[derive(serde::Serialize, serde::Deserialize)]
#[serde(deny_unknown_fields)]
struct AnthropicRequest {
    model: String,
    max_tokens: u32,
    messages: Vec<AnthropicMessage>,
    stream: bool,
}
#[derive(serde::Serialize, serde::Deserialize)]
#[serde(deny_unknown_fields)]
struct AnthropicMessage {
    role: String,
    content: String,
}

// Go unmarshals omitted/null scalar fields to their zero values; CoreKit's
// derived Rust GenerateResponse requires all three. Preserve streaming by
// adapting one bounded NDJSON line at a time, not buffering the response.
struct GoGenerateDefaults<R> {
    input: BufReader<R>,
    pending: Cursor<Vec<u8>>,
}
impl<R: Read> Read for GoGenerateDefaults<R> {
    fn read(&mut self, output: &mut [u8]) -> io::Result<usize> {
        if output.is_empty() {
            return Ok(0);
        }
        if self.pending.position() < self.pending.get_ref().len() as u64 {
            return self.pending.read(output);
        }
        let mut line = Vec::new();
        let n = self
            .input
            .by_ref()
            .take(4 * 1024 * 1024 + 1)
            .read_until(b'\n', &mut line)?;
        if n == 0 {
            return Ok(0);
        }
        if n >= 4 * 1024 * 1024 {
            return Err(io::Error::other("bufio.Scanner: token too long"));
        }
        if let Ok(mut value) = serde_json::from_slice::<serde_json::Value>(&line) {
            if value.is_null() {
                value = serde_json::json!({});
            }
            if let Some(map) = value.as_object_mut() {
                for (key, default) in [
                    ("model", serde_json::json!("")),
                    ("response", serde_json::json!("")),
                    ("done", serde_json::json!(false)),
                ] {
                    let field = map.entry(key).or_insert(default.clone());
                    if field.is_null() {
                        *field = default;
                    }
                }
                line = serde_json::to_vec(&value).map_err(io::Error::other)?;
                line.push(b'\n');
            }
        }
        self.pending = Cursor::new(line);
        self.pending.read(output)
    }
}
