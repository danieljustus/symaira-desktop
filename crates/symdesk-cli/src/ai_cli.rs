use std::{
    collections::BTreeMap,
    fs,
    io::{self, Read},
    path::{Path, PathBuf},
    process::ExitCode,
};

use clap::{Arg, Command};
use serde::Serialize;
use symaira_core_exit::ExitCode as CoreExitCode;
use symdesk_index::{SearchSource, SourceRegistry, open_for_vault};

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
    let config = match load_config() {
        Ok(config) => config,
        Err(error) => return super::emit_error(error, output_json),
    };
    let provider = if config.llm_provider.is_empty() {
        "ollama"
    } else {
        config.llm_provider.as_str()
    };
    if provider != "ollama" || !config.ollama_url.is_empty() {
        return super::emit_error(
            format!("Rust ask provider {provider:?} is not implemented"),
            output_json,
        );
    }
    let sidecar = match open_for_vault(&root) {
        Ok(sidecar) => sidecar,
        Err(error) => return super::emit_error(error.to_string(), output_json),
    };
    let sources = match SourceRegistry::open(&root).and_then(|registry| registry.list()) {
        Ok(sources) => sources,
        Err(error) => return super::emit_error(error.to_string(), output_json),
    };

    let hits = if query.trim().is_empty() {
        Vec::new()
    } else {
        match super::search_cli::hybrid_search(&root, query, &sources, &sidecar) {
            Ok(Some(hits)) => hits,
            Ok(None) | Err(_) => match sidecar.search_plan(&root, query) {
                Ok(response) => super::search_cli::lexical_hits(response.results, &sources),
                Err(error) => return super::emit_error(error.to_string(), output_json),
            },
        }
    };

    let mut events = vec![AskEvent::tool("running"), AskEvent::tool("done")];
    let paths = hits
        .iter()
        .map(|hit| display_path(&root, &hit.path, &sources))
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
    events.push(AskEvent::answer(
        "⚠️ **AI feature not configured.**\n\nSet your Ollama endpoint in Settings → AI.\n\nHere are the most relevant search results from your vault:\n\n",
    ));
    for path in paths.iter().take(3) {
        events.push(AskEvent::answer(&format!("- [[{path}]]\n")));
    }
    events.push(AskEvent::tool("done"));
    if let Some(event) = events.last_mut() {
        event.tool_name = Some("llm".to_owned());
    }
    events.push(AskEvent::done());
    emit_ask_events(&events, output_json)
}

fn display_path(root: &Path, path: &str, sources: &[SearchSource]) -> String {
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

    let message = if text.trim().is_empty() {
        "⚠️ No text provided – please select text first.\n".to_owned()
    } else {
        match load_config() {
            Ok(config) => match config.llm_provider.as_str() {
                "anthropic" if !config.has_api_key() => {
                    "⚠️ **AI feature not configured.**\n\nAnthropic API key could not be resolved (missing secret via symvault or environment variable).\n".to_owned()
                }
                "ollama" if config.ollama_url.is_empty() => {
                    "⚠️ **AI feature not configured.**\n\nSet your Ollama endpoint in Settings → AI.\n".to_owned()
                }
                provider => format!(
                    "⚠️ Request failed: Rust transform provider {provider:?} is not implemented.\n"
                ),
            },
            Err(error) => format!("⚠️ Request failed: {error}\n"),
        }
    };

    emit_chunk(&message, output_json)
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
    if output_json {
        match serde_json::to_string(&TransformChunk { chunk }) {
            Ok(json) => super::write_stdout(format!("{}\n", super::go_escape_json(json))),
            Err(error) => super::write_stderr(&format!("{error}\n"), CoreExitCode::Generic),
        }
    } else {
        super::write_stdout(format!("{{{chunk}}}\n"))
    }
}
