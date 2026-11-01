//! Go `internal/ai` composition: Ask/Transform/PromptOne prompt building,
//! honest degradation and the optional Hermes CLI backend, over the CoreKit
//! provider boundary in `symdesk_core::ai`.
//!
//! Secret resolution is injected; nothing here reads a keychain, an ambient
//! API key or operator state. The Hermes backend is an explicit child process
//! (`hermes -z <prompt> [--resume <session>]`) found on the `PATH` snapshot.

use std::{
    collections::BTreeMap, ffi::OsStr, os::unix::ffi::OsStrExt, path::PathBuf, process::Command,
    time::Duration,
};

use symdesk_core::{
    ai::{NOT_CONFIGURED, Provider, prompt_one as core_prompt_one},
    cancel::CancelToken,
    compose::Resolver,
    config::Config,
};

use crate::{gofmt::status_text, process::run_cancellable};

const HERMES_BINARY: &str = "hermes";
/// Go `hermesTimeout`; rendered as `5m0s` in diagnostics.
pub const HERMES_TIMEOUT: Duration = Duration::from_secs(5 * 60);

/// One retrieved note excerpt (`{path,title,snippet}`).
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Doc {
    pub path: String,
    pub title: String,
    pub snippet: String,
}

/// Inputs shared by every entry point. `child_env` is the full environment
/// handed to a Hermes child; `None` inherits this process's, like Go.
pub struct Context<'a> {
    pub config: &'a Config,
    pub environment: &'a BTreeMap<String, String>,
    pub child_env: Option<&'a BTreeMap<String, String>>,
    pub resolve_key: &'a mut dyn FnMut(&str) -> String,
    pub hermes_timeout: Duration,
    /// Cancels the in-flight provider call (Go's `context` cancellation); once
    /// cancelled nothing more is delivered.
    pub cancel: Option<&'a CancelToken>,
}

impl<'a> Context<'a> {
    #[must_use]
    pub fn new(
        config: &'a Config,
        environment: &'a BTreeMap<String, String>,
        resolve_key: &'a mut dyn FnMut(&str) -> String,
    ) -> Self {
        Self {
            config,
            environment,
            child_env: None,
            resolve_key,
            hermes_timeout: HERMES_TIMEOUT,
            cancel: None,
        }
    }

    fn cancelled(&self) -> bool {
        self.cancel.is_some_and(CancelToken::is_cancelled)
    }
}

/// Go `buildPrompt`: ground the model in at most five excerpts. Snippets are
/// cut at 1500 *bytes*, which may split a rune; the prompt is therefore bytes.
#[must_use]
pub fn build_ask_prompt(language: &str, query: &str, docs: &[Doc]) -> Vec<u8> {
    let mut prompt: Vec<u8> = b"You are the assistant of a local Markdown vault. Answer the question exclusively based on the following note excerpts. If the excerpts do not contain the answer, say so honestly. Refer to notes as [[path]].".to_vec();
    if language.is_empty() {
        prompt.extend_from_slice(b" Answer in the language of the query.\n\n");
    } else {
        prompt.extend_from_slice(format!(" Answer in {language}.\n\n").as_bytes());
    }
    for doc in docs.iter().take(5) {
        let snippet = doc.snippet.as_bytes();
        let snippet = &snippet[..snippet.len().min(1500)];
        prompt.extend_from_slice(
            format!("--- Note [[{}]] ({}) ---\n", doc.path, doc.title).as_bytes(),
        );
        prompt.extend_from_slice(snippet);
        prompt.extend_from_slice(b"\n\n");
    }
    prompt.extend_from_slice(format!("Question: {query}\n").as_bytes());
    prompt
}

/// Go `buildTransformPrompt`; unknown intents fall back to rewrite.
#[must_use]
pub fn build_transform_prompt(language: &str, text: &str, intent: &str) -> String {
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
    let language = if language.is_empty() {
        "the language of the input text"
    } else {
        language
    };
    format!("{instruction} Answer in {language} as pure Markdown text.\n\n---\n{text}\n---\n")
}

pub use symdesk_core::gofmt::lossy as go_lossy;

fn provider_name(config: &Config) -> &str {
    if config.llm_provider.is_empty() {
        "ollama"
    } else {
        &config.llm_provider
    }
}

/// Go `streamLLM`: dispatch one prompt, emitting chunks as they arrive.
fn stream(
    context: &mut Context<'_>,
    prompt: &[u8],
    emit: &mut dyn FnMut(&str),
) -> Result<(), String> {
    if context.config.llm_provider == "hermes" {
        let answer = hermes(context, prompt)?;
        if !context.cancelled() {
            emit(&answer);
        }
        return Ok(());
    }
    let provider = Provider::resolve(
        context.config,
        context.environment,
        &mut *context.resolve_key,
    )?;
    let text = go_lossy(prompt);
    let cancel = context.cancel;
    let deliver = |chunk: &str| {
        if cancel.is_none_or(|token| !token.is_cancelled()) {
            emit(chunk);
        }
        Ok(())
    };
    match cancel {
        Some(token) => provider.stream_cancellable(&text, token, deliver),
        None => provider.stream(&text, deliver),
    }
}

/// Go `streamHermes`: one final answer, no token streaming.
fn hermes(context: &Context<'_>, prompt: &[u8]) -> Result<String, String> {
    let resolver = Resolver {
        home: context.environment.get("HOME").cloned().unwrap_or_default(),
        symaira_bin: String::new(),
        path: context.environment.get("PATH").cloned().unwrap_or_default(),
        cwd: std::env::current_dir().map_err(|error| error.to_string())?,
        allow_relative_path: false,
    };
    let Ok(binary) = resolver.look_path(HERMES_BINARY) else {
        return Err(NOT_CONFIGURED.to_owned());
    };
    let mut command = Command::new(PathBuf::from(binary));
    command.arg("-z").arg(OsStr::from_bytes(prompt));
    if !context.config.hermes_session.is_empty() {
        command.arg("--resume").arg(&context.config.hermes_session);
    }
    if let Some(environment) = context.child_env {
        command.env_clear().envs(environment);
    }
    let cancel = context.cancel;
    let finished = run_cancellable(command, context.hermes_timeout, &|| {
        cancel.is_some_and(CancelToken::is_cancelled)
    })
    .map_err(|error| format!("hermes backend failed: {error}"))?;
    if finished.timed_out || finished.cancelled {
        return Err("hermes backend timed out after 5m0s".to_owned());
    }
    let (status, stdout, stderr) = (finished.status, finished.stdout, finished.stderr);
    if !status.success() {
        let message = String::from_utf8_lossy(&stderr).trim().to_owned();
        let message = if message.is_empty() {
            status_text(status)
        } else {
            message
        };
        return Err(format!("hermes backend failed: {message}"));
    }
    let answer = String::from_utf8_lossy(&stdout).trim().to_owned();
    if answer.is_empty() {
        return Err("hermes backend returned an empty answer".to_owned());
    }
    Ok(answer)
}

fn not_configured_chunk(config: &Config) -> &'static str {
    if provider_name(config) == "anthropic" {
        "⚠️ **AI feature not configured.**\n\nAnthropic API key could not be resolved (missing secret via symvault or environment variable).\n"
    } else {
        "⚠️ **AI feature not configured.**\n\nSet your Ollama endpoint in Settings → AI.\n"
    }
}

/// Go `ai.Ask`: stream an answer grounded in `docs`, degrading honestly when
/// no provider is configured. Chunks are delivered through `emit`.
pub fn ask(context: &mut Context<'_>, query: &str, docs: &[Doc], emit: &mut dyn FnMut(&str)) {
    let prompt = build_ask_prompt(&context.config.language, query, docs);
    let result = stream(context, &prompt, emit);
    if context.cancelled() {
        return;
    }
    match result {
        Ok(()) => {}
        Err(error) if error == NOT_CONFIGURED => {
            if provider_name(context.config) == "anthropic" {
                emit(not_configured_chunk(context.config));
            } else {
                emit(
                    "⚠️ **AI feature not configured.**\n\nSet your Ollama endpoint in Settings → AI.\n\nHere are the most relevant search results from your vault:\n\n",
                );
                for doc in docs.iter().take(3) {
                    emit(&format!("- [[{}]]\n", doc.path));
                }
            }
        }
        Err(error) => emit(&format!(
            "⚠️ Request failed: {}\n",
            crate::gofmt::go_transport_error(&error, &context.config.ollama_url)
        )),
    }
}

/// Go `ai.Transform`: `summarize`, `rewrite` or `continue` over the given text
/// only; it never touches the vault.
pub fn transform(context: &mut Context<'_>, text: &str, intent: &str, emit: &mut dyn FnMut(&str)) {
    let text = text.trim();
    if text.is_empty() {
        emit("⚠️ No text provided – please select text first.\n");
        return;
    }
    let prompt = build_transform_prompt(&context.config.language, text, intent);
    let result = stream(context, prompt.as_bytes(), emit);
    if context.cancelled() {
        return;
    }
    match result {
        Ok(()) => {}
        Err(error) if error == NOT_CONFIGURED => emit(not_configured_chunk(context.config)),
        Err(error) => emit(&format!(
            "⚠️ Request failed: {}\n",
            crate::gofmt::go_transport_error(&error, &context.config.ollama_url)
        )),
    }
}

/// Go `ai.PromptOne`: the complete trimmed response of one prompt.
///
/// # Errors
///
/// Go's one-shot diagnostics: provider-specific "not configured" text, or the
/// provider error.
pub fn prompt_one(context: &mut Context<'_>, prompt: &str) -> Result<String, String> {
    if context.config.llm_provider != "hermes" {
        return core_prompt_one(
            context.config,
            context.environment,
            &mut *context.resolve_key,
            prompt,
        );
    }
    match hermes(context, prompt.as_bytes()) {
        Ok(answer) => Ok(answer),
        Err(error) if error == NOT_CONFIGURED => Err("SYMDESK_OLLAMA_URL not set".to_owned()),
        Err(error) => Err(error),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn go_lossy_replaces_each_invalid_byte() {
        let truncated = &"é€".as_bytes()[..4]; // 0xC3 0xA9 0xE2 0x82
        assert_eq!(go_lossy(truncated), "é\u{FFFD}\u{FFFD}");
        assert_eq!(go_lossy(b"ok"), "ok");
    }

    #[test]
    fn ask_prompt_truncates_snippets_at_1500_bytes_and_five_docs() {
        let doc = |n: usize| Doc {
            path: format!("{n}.md"),
            title: "T".to_owned(),
            snippet: format!("a{}", "é".repeat(800)),
        };
        let docs: Vec<Doc> = (0..6).map(doc).collect();
        let prompt = build_ask_prompt("", "Q", &docs);
        let text = go_lossy(&prompt);
        assert_eq!(text.matches("--- Note [[").count(), 5);
        assert!(!text.contains("[[5.md]]"));
        assert!(text.ends_with("Question: Q\n"));
        // 1 + 749 whole runes fill 1499 bytes; byte 1500 is a dangling lead byte.
        assert_eq!(text.matches('\u{FFFD}').count(), 5);
    }
}
