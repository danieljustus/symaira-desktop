use std::{
    collections::BTreeMap,
    fs,
    io::{self, Read},
    path::PathBuf,
    process::ExitCode,
};

use clap::{Arg, Command};
use serde::Serialize;
use symaira_core_exit::ExitCode as CoreExitCode;

#[derive(Serialize)]
struct TransformChunk<'a> {
    chunk: &'a str,
}

pub fn transform_cli() -> Command {
    Command::new("transform")
        .arg(Arg::new("intent").required(true))
        .arg(Arg::new("text").long("text").num_args(1))
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
