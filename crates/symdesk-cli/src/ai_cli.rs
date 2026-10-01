use std::{
    cell::RefCell,
    collections::BTreeMap,
    fs,
    io::{self, Read, Write},
    path::{Path, PathBuf},
    process::ExitCode,
    time::Duration,
};

use clap::{Arg, Command};
use serde::Serialize;
use symaira_core_exit::ExitCode as CoreExitCode;
use symaira_core_llm::{CancellationToken, ChatOptions, ClientBuilder, Message, lookup};
use symdesk_index::{SearchSource, SourceRegistry, open_for_vault};

const DEFAULT_ANTHROPIC_MODEL: &str = "claude-sonnet-5";

#[derive(Serialize)]
struct TransformChunk<'a> {
    chunk: &'a str,
}

pub fn transform_cli() -> Command {
    Command::new("transform")
        .arg(Arg::new("intent").required(true))
        .arg(Arg::new("text").long("text").num_args(1))
}

#[derive(Serialize)]
struct AskEvent {
    #[serde(rename = "type")]
    kind: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    text: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    path: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    title: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    snippet: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    score: Option<f64>,
    #[serde(skip_serializing_if = "Option::is_none")]
    tool_name: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    status: Option<String>,
}

impl AskEvent {
    fn tool(status: &str) -> Self {
        Self {
            kind: "tool".to_owned(),
            text: None,
            path: None,
            title: None,
            snippet: None,
            score: None,
            tool_name: Some("search".to_owned()),
            status: Some(status.to_owned()),
        }
    }

    fn citation(path: &str, title: &str, snippet: &str, score: f64) -> Self {
        Self {
            kind: "citation".to_owned(),
            text: None,
            path: Some(path.to_owned()),
            title: Some(title.to_owned()),
            snippet: (!snippet.is_empty()).then(|| snippet.to_owned()),
            score: (score != 0.0).then_some(score),
            tool_name: None,
            status: None,
        }
    }

    fn answer(text: &str) -> Self {
        Self {
            kind: "answer".to_owned(),
            text: Some(text.to_owned()),
            path: None,
            title: None,
            snippet: None,
            score: None,
            tool_name: None,
            status: None,
        }
    }

    fn done() -> Self {
        Self {
            kind: "done".to_owned(),
            text: None,
            path: None,
            title: None,
            snippet: None,
            score: None,
            tool_name: None,
            status: None,
        }
    }
}

pub fn ask_cli() -> Command {
    Command::new("ask").arg(Arg::new("query").required(true))
}

pub fn run_ask(command: &clap::ArgMatches, vault: Option<&str>, output_json: bool) -> ExitCode {
    let query = command
        .get_one::<String>("query")
        .map(String::as_str)
        .unwrap_or_default();
    let root = match super::resolve_vault(vault) {
        Ok(root) => root,
        Err(error) => return super::emit_error(error, output_json),
    };
    if let Err(error) = ensure_offline_ask_provider() {
        return super::emit_error(error, output_json);
    }
    let sidecar = match open_for_vault(&root) {
        Ok(sidecar) => sidecar,
        Err(error) => return super::emit_error(error.to_string(), output_json),
    };
    let sources = match SourceRegistry::open(&root).and_then(|registry| registry.list()) {
        Ok(sources) => sources,
        Err(error) => return super::emit_error(error.to_string(), output_json),
    };

    let hits = match search_for_ask(&root, query, None, &sources, &sidecar) {
        Ok(hits) => hits,
        Err(error) => return super::emit_error(error, output_json),
    };

    let mut events = vec![AskEvent::tool("running"), AskEvent::tool("done")];
    let paths = hits
        .iter()
        .map(|hit| ask_display_path(&root, &hit.path, &sources))
        .collect::<Vec<_>>();
    for (hit, path) in hits.iter().zip(&paths) {
        events.push(AskEvent::citation(
            path,
            &hit.title,
            &hit.snippet,
            hit.score,
        ));
    }
    events.push(AskEvent::tool("running"));
    if let Some(event) = events.last_mut() {
        event.tool_name = Some("llm".to_owned());
    }
    for chunk in offline_ask_chunks(&paths) {
        events.push(AskEvent::answer(&chunk));
    }
    events.push(AskEvent::tool("done"));
    if let Some(event) = events.last_mut() {
        event.tool_name = Some("llm".to_owned());
    }
    events.push(AskEvent::done());
    emit_ask_events(&events, output_json)
}

pub(crate) fn ask_display_path(root: &Path, path: &str, sources: &[SearchSource]) -> String {
    if sources
        .iter()
        .any(|source| Path::new(path).starts_with(&source.path))
    {
        path.to_owned()
    } else {
        super::relative_path(root, path)
    }
}

fn emit_ask_events(events: &[AskEvent], output_json: bool) -> ExitCode {
    let mut output = String::new();
    for event in events {
        if output_json {
            match serde_json::to_string(event) {
                Ok(event) => output.push_str(&format!("{}\n", super::go_escape_json(event))),
                Err(error) => {
                    return super::write_stderr(&format!("{error}\n"), CoreExitCode::Generic);
                }
            }
        } else if let Some(text) = &event.text {
            output.push_str(text);
        }
    }
    super::write_stdout(output)
}

pub fn run_transform(command: &clap::ArgMatches, output_json: bool) -> ExitCode {
    let mut text = command
        .get_one::<String>("text")
        .cloned()
        .unwrap_or_default();
    if text.is_empty() {
        let mut bytes = Vec::new();
        if let Err(error) = io::stdin().read_to_end(&mut bytes) {
            return super::write_stderr(&format!("{error}\n"), CoreExitCode::Generic);
        }
        text = String::from_utf8_lossy(&bytes).into_owned();
    }

    if text.trim().is_empty() {
        return emit_chunk(
            "⚠️ No text provided – please select text first.\n",
            output_json,
        );
    }
    let config = match load_config() {
        Ok(config) => config,
        Err(error) => return emit_chunk(&format!("⚠️ Request failed: {error}\n"), output_json),
    };
    if config.llm_provider == "anthropic" {
        let base_url = std::env::var("SYMDESK_ANTHROPIC_URL")
            .ok()
            .filter(|value| !value.is_empty());
        return run_anthropic_transform(
            command,
            &config,
            text.trim(),
            output_json,
            base_url.as_deref(),
        );
    }

    // Ollama and Hermes keep their existing visible fallback behavior in this slice.
    let message = match config.llm_provider.as_str() {
        "ollama" if config.ollama_url.is_empty() => {
            "⚠️ **AI feature not configured.**\n\nSet your Ollama endpoint in Settings → AI.\n"
                .to_owned()
        }
        provider => {
            format!("⚠️ Request failed: Rust transform provider {provider:?} is not implemented.\n")
        }
    };
    emit_chunk(&message, output_json)
}

pub(crate) fn ensure_offline_ask_provider() -> Result<(), String> {
    let config = load_config()?;
    let provider = if config.llm_provider.is_empty() {
        "ollama"
    } else {
        config.llm_provider.as_str()
    };
    if provider != "ollama" || !config.ollama_url.is_empty() {
        return Err(format!("Rust ask provider {provider:?} is not implemented"));
    }
    Ok(())
}

pub(crate) fn search_for_ask(
    vault: &std::path::Path,
    query: &str,
    notebook: Option<&str>,
    sources: &[SearchSource],
    sidecar: &symdesk_index::Sidecar,
) -> Result<Vec<crate::search_cli::CliSearchHit>, String> {
    if let Some(notebook) = notebook {
        let (hits, _) =
            symdesk_protocol::search_notebook_ask_sources(vault, notebook, query, sidecar)?;
        return Ok(hits
            .into_iter()
            .map(|hit| crate::search_cli::CliSearchHit {
                path: hit.path,
                title: hit.title,
                snippet: hit.snippet,
                score: hit.score,
                anchor: None,
                metadata_matches: Vec::new(),
                source_type: None,
                read_only: false,
            })
            .collect());
    }
    if query.trim().is_empty() {
        return Ok(Vec::new());
    }
    match crate::search_cli::hybrid_search(vault, query, sources, sidecar) {
        Ok(Some(hits)) => Ok(hits),
        Ok(None) | Err(_) => sidecar
            .search_plan(vault, query)
            .map(|response| crate::search_cli::lexical_hits(response.results, sources))
            .map_err(|error| error.to_string()),
    }
}

pub(crate) fn offline_ask_chunks(paths: &[String]) -> Vec<String> {
    let mut chunks = vec![
        "⚠️ **AI feature not configured.**\n\nSet your Ollama endpoint in Settings → AI.\n\nHere are the most relevant search results from your vault:\n\n".to_owned(),
    ];
    chunks.extend(paths.iter().take(3).map(|path| format!("- [[{path}]]\n")));
    chunks
}

fn run_anthropic_transform(
    command: &clap::ArgMatches,
    config: &symdesk_core::config::Config,
    text: &str,
    output_json: bool,
    base_url: Option<&str>,
) -> ExitCode {
    let api_key = crate::ai_secrets::resolve_key(config.api_key_reference());
    if api_key.is_empty() {
        return emit_chunk(
            "⚠️ **AI feature not configured.**\n\nAnthropic API key could not be resolved (missing secret via symvault or environment variable).\n",
            output_json,
        );
    }
    let intent = command
        .get_one::<String>("intent")
        .map(String::as_str)
        .unwrap_or_default();
    let prompt = build_transform_prompt(config, text, intent);
    let writer = RefCell::new(io::stdout());
    let mut output_failed = false;
    let result = stream_anthropic_transform(config, &api_key, base_url, &prompt, |chunk| {
        if !output_failed
            && emit_transform_chunk(&mut *writer.borrow_mut(), chunk, output_json).is_err()
        {
            output_failed = true;
        }
        if output_failed {
            Err(symaira_core_llm::Error {
                code: symaira_core_llm::ErrorCode::TransportError,
                status_code: 0,
                body: String::new(),
                retry_after: String::new(),
                detail: "transform output write failed".to_owned(),
            })
        } else {
            Ok(())
        }
    });
    if output_failed {
        return super::process_exit(CoreExitCode::Generic);
    }
    match result {
        Ok(()) => super::process_exit(CoreExitCode::Ok),
        Err(error) => emit_chunk(&transform_failure_message(&error, &api_key), output_json),
    }
}

fn transform_failure_message(error: &AnthropicTransformError, api_key: &str) -> String {
    let detail = match error {
        AnthropicTransformError::Client(error) | AnthropicTransformError::Stream(error) => {
            error.to_string()
        }
    };
    let detail = if api_key.is_empty() {
        detail
    } else {
        detail.replace(api_key, "[REDACTED]")
    };
    match error {
        AnthropicTransformError::Client(_) => format!("⚠️ Request failed: {detail}\n"),
        AnthropicTransformError::Stream(_) => format!("⚠️ Request failed: anthropic: {detail}\n"),
    }
}

fn anthropic_model(config: &symdesk_core::config::Config) -> &str {
    if config.llm_model.is_empty() {
        DEFAULT_ANTHROPIC_MODEL
    } else {
        &config.llm_model
    }
}

fn build_transform_prompt(
    config: &symdesk_core::config::Config,
    text: &str,
    intent: &str,
) -> String {
    let instruction = match intent {
        "summarize" => {
            "Summarize the following text concisely. Return only the summary, without introductory remarks."
        }
        "continue" => {
            "Continue the following text in a meaningful way, keeping the same style and tone. Return only the continuation, not the original text."
        }
        _ => {
            "Rewrite the following text more clearly and fluently, without changing its meaning. Return only the revised text."
        }
    };
    let language = if config.language.is_empty() {
        "the language of the input text"
    } else {
        config.language.as_str()
    };
    format!("{instruction} Answer in {language} as pure Markdown text.\n\n---\n{text}\n---\n")
}

fn stream_anthropic_transform(
    config: &symdesk_core::config::Config,
    api_key: &str,
    base_url: Option<&str>,
    prompt: &str,
    mut on_chunk: impl FnMut(&str) -> symaira_core_llm::Result<()>,
) -> Result<(), AnthropicTransformError> {
    let descriptor = lookup("anthropic").expect("CoreKit embeds the Anthropic descriptor");
    let mut builder = ClientBuilder::new(descriptor.clone(), "")
        .api_key(api_key)
        .timeout(Duration::from_secs(5 * 60));
    if let Some(base_url) = base_url {
        builder = builder.base_url(base_url);
    }
    let client = builder.build().map_err(AnthropicTransformError::Client)?;
    let model = anthropic_model(config);
    let max_tokens = if config.max_tokens > 0 {
        u32::try_from(config.max_tokens).unwrap_or(u32::MAX)
    } else {
        8192
    };
    let callback = RefCell::new(&mut on_chunk);
    let cancellation = CancellationToken::new();
    let runtime = tokio::runtime::Builder::new_current_thread()
        .enable_all()
        .build()
        .map_err(|error| {
            AnthropicTransformError::Client(symaira_core_llm::Error {
                code: symaira_core_llm::ErrorCode::ProviderError,
                status_code: 0,
                body: String::new(),
                retry_after: String::new(),
                detail: format!("failed to initialize Anthropic runtime: {error}"),
            })
        })?;
    runtime
        .block_on(client.stream_chat_cancellable(
            &cancellation,
            model,
            &[Message {
                role: "user".to_owned(),
                content: prompt.to_owned(),
            }],
            Some(&ChatOptions {
                max_tokens,
                ..ChatOptions::default()
            }),
            |delta| {
                callback.borrow_mut()(delta)?;
                Ok(())
            },
            |reason| {
                if reason == "max_tokens"
                    && callback.borrow_mut()("\n\n⚠️ **[Output truncated due to token limit]**")
                        .is_err()
                {
                    cancellation.cancel();
                }
            },
        ))
        .map_err(AnthropicTransformError::Stream)
}

#[derive(Debug)]
enum AnthropicTransformError {
    Client(symaira_core_llm::Error),
    Stream(symaira_core_llm::Error),
}

fn load_config() -> Result<symdesk_core::config::Config, String> {
    let environment = std::env::vars().collect::<BTreeMap<_, _>>();
    let path = PathBuf::from(symdesk_core::config::global_path(&environment));
    let input = match fs::read_to_string(path) {
        Ok(input) => Some(input),
        Err(error) if error.kind() == io::ErrorKind::NotFound => None,
        Err(error) => return Err(format!("failed to read config file: {error}")),
    };
    symdesk_core::config::load(input.as_deref(), &environment)
}

fn emit_chunk(chunk: &str, output_json: bool) -> ExitCode {
    match emit_transform_chunk(&mut io::stdout(), chunk, output_json) {
        Ok(()) => super::process_exit(CoreExitCode::Ok),
        Err(_) => super::process_exit(CoreExitCode::Generic),
    }
}

fn emit_transform_chunk(writer: &mut impl Write, chunk: &str, output_json: bool) -> io::Result<()> {
    if output_json {
        let encoded = serde_json::to_string(&TransformChunk { chunk }).map_err(io::Error::other)?;
        writeln!(writer, "{}", super::go_escape_json(encoded))?;
    } else {
        writeln!(writer, "{{{chunk}}}")?;
    }
    writer.flush()
}

#[cfg(test)]
mod tests {
    use super::{
        AnthropicTransformError, DEFAULT_ANTHROPIC_MODEL, anthropic_model, build_transform_prompt,
        emit_transform_chunk, stream_anthropic_transform, transform_failure_message,
    };
    use std::{
        io::{Read, Write},
        net::TcpListener,
        thread,
    };

    #[test]
    fn transform_prompt_matches_go_language_and_intent_contract() {
        let mut config = symdesk_core::config::Config::default();
        config.language = "de".to_owned();
        assert_eq!(
            build_transform_prompt(&config, "Quelle", "summarize"),
            "Summarize the following text concisely. Return only the summary, without introductory remarks. Answer in de as pure Markdown text.\n\n---\nQuelle\n---\n"
        );
        assert!(build_transform_prompt(&config, "Quelle", "continue").starts_with(
            "Continue the following text in a meaningful way, keeping the same style and tone. Return only the continuation, not the original text."
        ));
        assert!(build_transform_prompt(&config, "Quelle", "unknown").starts_with(
            "Rewrite the following text more clearly and fluently, without changing its meaning. Return only the revised text."
        ));
    }

    #[test]
    fn empty_anthropic_model_uses_go_default_instead_of_corekit_descriptor_default() {
        let config = symdesk_core::config::Config::default();
        let mut config = config;
        config.llm_model.clear();
        assert_eq!(anthropic_model(&config), DEFAULT_ANTHROPIC_MODEL);
    }

    #[test]
    fn transform_chunk_bytes_keep_go_json_escaping_and_plain_braces() {
        let mut json = Vec::new();
        emit_transform_chunk(&mut json, "<&>\u{2028}", true).expect("write JSON chunk");
        assert_eq!(json, b"{\"chunk\":\"\\u003c\\u0026\\u003e\\u2028\"}\n");

        let mut plain = Vec::new();
        emit_transform_chunk(&mut plain, "first", false).expect("write plain chunk");
        emit_transform_chunk(&mut plain, "second", false).expect("write second plain chunk");
        assert_eq!(plain, b"{first}\n{second}\n");
    }

    #[test]
    fn anthropic_transport_streams_corekit_response_and_preserves_request_contract() {
        let listener = TcpListener::bind("127.0.0.1:0").expect("bind local provider");
        let address = listener.local_addr().expect("read local provider address");
        let provider = thread::spawn(move || {
            let (mut stream, _) = listener.accept().expect("accept provider request");
            let request = read_http_request(&mut stream);
            let body = request.split_once("\r\n\r\n").expect("HTTP body").1;
            let request_json: serde_json::Value =
                serde_json::from_str(body).expect("Anthropic request JSON");
            assert!(request.starts_with("POST /messages HTTP/1.1\r\n"));
            assert!(
                request
                    .to_ascii_lowercase()
                    .contains("x-api-key: test-key\r\n")
            );
            assert!(
                request
                    .to_ascii_lowercase()
                    .contains("anthropic-version: 2023-06-01\r\n")
            );
            assert_eq!(request_json["model"], "model-from-config");
            assert_eq!(request_json["max_tokens"], 4096);
            assert_eq!(request_json["stream"], true);
            assert_eq!(request_json["messages"][0]["content"], "exact prompt");

            let response = concat!(
                "data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"one\"}}\n\n",
                "data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"two\"}}\n\n",
                "data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"max_tokens\"}}\n\n",
                "data: [DONE]\n\n"
            );
            write!(
                stream,
                "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{}",
                response.len(),
                response
            )
            .expect("write stream response");
        });

        let mut config = symdesk_core::config::Config::default();
        config.llm_model = "model-from-config".to_owned();
        config.max_tokens = 4096;
        let mut chunks = Vec::new();
        stream_anthropic_transform(
            &config,
            "test-key",
            Some(&format!("http://{address}")),
            "exact prompt",
            |chunk| {
                chunks.push(chunk.to_owned());
                Ok(())
            },
        )
        .expect("CoreKit streams provider events");
        provider.join().expect("provider assertions pass");
        assert_eq!(
            chunks,
            [
                "one",
                "two",
                "\n\n⚠️ **[Output truncated due to token limit]**"
            ]
        );
    }

    #[test]
    fn anthropic_http_error_is_reported_by_corekit() {
        let listener = TcpListener::bind("127.0.0.1:0").expect("bind local provider");
        let address = listener.local_addr().expect("read local provider address");
        let provider = thread::spawn(move || {
            let (mut stream, _) = listener.accept().expect("accept provider request");
            let _request = read_http_request(&mut stream);
            let response =
                r#"{"error":{"type":"api_error","message":"temporary failure test-key"}}"#;
            write!(
                stream,
                "HTTP/1.1 500 Internal Server Error\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{}",
                response.len(),
                response
            )
            .expect("write error response");
        });
        let config = symdesk_core::config::Config::default();
        let error = stream_anthropic_transform(
            &config,
            "test-key",
            Some(&format!("http://{address}")),
            "prompt",
            |_| panic!("failed responses do not yield chunks"),
        )
        .expect_err("provider status is an error");
        provider.join().expect("provider request completed");
        let AnthropicTransformError::Stream(error) = error else {
            panic!("HTTP errors are stream errors");
        };
        assert!(error.to_string().contains("status 500"));
        assert!(error.to_string().contains("temporary failure test-key"));
        let message =
            transform_failure_message(&AnthropicTransformError::Stream(error.clone()), "test-key");
        assert!(message.starts_with("⚠️ Request failed: anthropic:"));
        assert!(!message.contains("test-key"));
    }

    #[test]
    fn anthropic_client_build_error_keeps_go_unwrapped_error_prefix() {
        let config = symdesk_core::config::Config::default();
        let error = stream_anthropic_transform(
            &config,
            "test-key",
            Some("http://provider.example.invalid"),
            "prompt",
            |_| Ok(()),
        )
        .expect_err("credentialed non-loopback HTTP is rejected at client build");
        let AnthropicTransformError::Client(error) = error else {
            panic!("base URL errors come from client construction");
        };
        let output =
            transform_failure_message(&AnthropicTransformError::Client(error.clone()), "test-key");
        assert!(!output.contains("Request failed: anthropic:"));
        assert!(output.contains(&error.to_string()));
    }

    #[test]
    fn output_callback_error_stops_corekit_streaming() {
        let listener = TcpListener::bind("127.0.0.1:0").expect("bind local provider");
        let address = listener.local_addr().expect("read local provider address");
        let provider = thread::spawn(move || {
            let (mut stream, _) = listener.accept().expect("accept provider request");
            let _request = read_http_request(&mut stream);
            let response = concat!(
                "data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"one\"}}\n\n",
                "data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"two\"}}\n\n"
            );
            write!(
                stream,
                "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{}",
                response.len(),
                response
            )
            .expect("write stream response");
        });
        let config = symdesk_core::config::Config::default();
        let mut calls = 0;
        let error = stream_anthropic_transform(
            &config,
            "test-key",
            Some(&format!("http://{address}")),
            "prompt",
            |_| {
                calls += 1;
                Err(symaira_core_llm::Error {
                    code: symaira_core_llm::ErrorCode::TransportError,
                    status_code: 0,
                    body: String::new(),
                    retry_after: String::new(),
                    detail: "output closed".to_owned(),
                })
            },
        )
        .expect_err("output failure aborts the CoreKit stream");
        provider.join().expect("provider request completed");
        assert_eq!(calls, 1);
        let AnthropicTransformError::Stream(error) = error else {
            panic!("callback failure stops a started stream");
        };
        assert!(error.to_string().contains("output closed"));
    }

    #[test]
    fn max_token_marker_write_error_cancels_a_stalled_provider_stream() {
        let listener = TcpListener::bind("127.0.0.1:0").expect("bind local provider");
        let address = listener.local_addr().expect("read local provider address");
        let provider = thread::spawn(move || {
            let (mut stream, _) = listener.accept().expect("accept provider request");
            let _request = read_http_request(&mut stream);
            let event =
                "data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"max_tokens\"}}\n\n";
            write!(
                stream,
                "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nTransfer-Encoding: chunked\r\nConnection: keep-alive\r\n\r\n{:x}\r\n{}\r\n",
                event.len(),
                event
            )
            .expect("send stop reason without ending the stream");
            stream.flush().expect("flush stop reason");
            stream
                .set_read_timeout(Some(std::time::Duration::from_secs(2)))
                .expect("bound cancellation wait");
            let mut byte = [0_u8; 1];
            let closed = matches!(stream.read(&mut byte), Ok(0) | Err(_));
            assert!(
                closed,
                "CoreKit cancellation must close the active response"
            );
        });
        let config = symdesk_core::config::Config::default();
        let error = stream_anthropic_transform(
            &config,
            "test-key",
            Some(&format!("http://{address}")),
            "prompt",
            |chunk| {
                assert!(chunk.contains("Output truncated"));
                Err(symaira_core_llm::Error {
                    code: symaira_core_llm::ErrorCode::TransportError,
                    status_code: 0,
                    body: String::new(),
                    retry_after: String::new(),
                    detail: "output closed".to_owned(),
                })
            },
        )
        .expect_err("failed truncation marker cancels the provider stream");
        provider.join().expect("provider observed connection close");
        assert!(matches!(error, AnthropicTransformError::Stream(_)));
    }

    fn read_http_request(stream: &mut impl Read) -> String {
        let mut request = Vec::new();
        while !request.ends_with(b"\r\n\r\n") {
            let mut byte = [0_u8; 1];
            stream.read_exact(&mut byte).expect("read HTTP headers");
            request.push(byte[0]);
        }
        let headers = String::from_utf8_lossy(&request);
        let content_length = headers
            .lines()
            .find_map(|line| {
                line.to_ascii_lowercase()
                    .strip_prefix("content-length: ")
                    .and_then(|value| value.trim().parse::<usize>().ok())
            })
            .unwrap_or_default();
        let mut body = vec![0; content_length];
        stream.read_exact(&mut body).expect("read request body");
        request.extend(body);
        String::from_utf8(request).expect("request is UTF-8")
    }
}
