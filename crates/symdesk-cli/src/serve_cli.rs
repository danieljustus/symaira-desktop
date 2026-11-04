//! `symdesk serve`: Go `cmd/symdesk/selfhost.go` `newServeCmd`.
//!
//! Flag defaults come from the environment, the Home Assistant add-on options
//! file (`/data/options.json`) overrides a default but never an explicit flag,
//! and the built-in local worker polls the server it runs inside.
//!
//! Precedence, exactly as Go resolves it:
//! * `--token`, `--worker-token`: flag (even empty) else the environment; an
//!   empty result falls back to the options file.
//! * `--listen`, `--worker-engine`, `--ollama-url`, `--ollama-model`,
//!   `--local-worker`: an explicit flag wins, then a non-empty options value
//!   (which therefore beats the environment default), then the environment,
//!   then the built-in default.
//! * vault: `--vault`, then the environment/config vault, then the options file.
//!
//! The options path is `/data/options.json`. Tests inject another path with the
//! internal argument [`take_internal_options_path`] strips before parsing.

use std::{
    ffi::OsString,
    path::{Path, PathBuf},
    process::ExitCode,
    sync::OnceLock,
};

use clap::{Arg, ArgAction, ArgMatches, Command, parser::ValueSource};
use serde_json::Value;
use symaira_core_exit::ExitCode as CoreExitCode;

use crate::{http, worker_cli};

const OPTIONS_PATH: &str = "/data/options.json";
/// Internal, undocumented test parameter (not an environment variable):
/// `--internal-ha-options=PATH` replaces `/data/options.json`.
const INTERNAL_OPTIONS_FLAG: &str = "--internal-ha-options=";

static OPTIONS_PATH_OVERRIDE: OnceLock<PathBuf> = OnceLock::new();

/// Removes the internal options-path argument before the cobra front half sees
/// the command line.
pub(crate) fn take_internal_options_path(args: &mut Vec<OsString>) {
    args.retain(|argument| {
        let Some(text) = argument.to_str() else {
            return true;
        };
        match text.strip_prefix(INTERNAL_OPTIONS_FLAG) {
            Some(path) => {
                let _ = OPTIONS_PATH_OVERRIDE.set(PathBuf::from(path));
                false
            }
            None => true,
        }
    });
}

pub(crate) fn command() -> Command {
    let value = |name: &'static str| {
        Arg::new(name)
            .long(name)
            .num_args(1)
            .action(ArgAction::Append)
    };
    Command::new("serve")
        .arg(value("listen"))
        .arg(value("token"))
        .arg(value("worker-token"))
        .arg(value("tls-cert"))
        .arg(value("tls-key"))
        .arg(
            Arg::new("local-worker")
                .long("local-worker")
                .num_args(0..=1)
                .require_equals(true)
                .default_missing_value("true")
                .action(ArgAction::Append),
        )
        .arg(value("worker-engine"))
        .arg(value("ollama-url"))
        .arg(value("ollama-model"))
        .arg(value("ocr-language"))
        // Go's `serve` has no `Args` validator: extra words are ignored.
        .arg(Arg::new("extra").num_args(0..).hide(true))
}

/// Go `homeAssistantOptions`.
#[derive(Debug, Default, PartialEq, Eq)]
pub(crate) struct HomeAssistantOptions {
    pub server_token: String,
    pub worker_token: String,
    pub listen: String,
    pub vault: String,
    pub local_processing: bool,
    pub worker_engine: String,
    pub ollama_url: String,
    pub ollama_model: String,
}

/// Go `readHomeAssistantOptionsFromFile`: an unreadable file or invalid JSON
/// yields empty options; a field of the wrong type is skipped while the other
/// fields are still decoded; keys match case-insensitively, the last one wins.
pub(crate) fn read_options(path: &Path) -> HomeAssistantOptions {
    let Ok(data) = std::fs::read(path) else {
        return HomeAssistantOptions::default();
    };
    parse_options(&data)
}

fn parse_options(data: &[u8]) -> HomeAssistantOptions {
    let mut options = HomeAssistantOptions::default();
    let Ok(entries) = ordered_entries(data) else {
        return options;
    };
    for (key, value) in entries {
        let text = |target: &mut String| {
            if let Value::String(text) = &value {
                target.clone_from(text);
            }
        };
        match key.to_lowercase().as_str() {
            "server_token" => text(&mut options.server_token),
            "worker_token" => text(&mut options.worker_token),
            "listen" => text(&mut options.listen),
            "vault" => text(&mut options.vault),
            "local_processing" => {
                if let Value::Bool(flag) = value {
                    options.local_processing = flag;
                }
            }
            "worker_engine" => text(&mut options.worker_engine),
            "ollama_url" => text(&mut options.ollama_url),
            "ollama_model" => text(&mut options.ollama_model),
            _ => {}
        }
    }
    options
}

/// The members of a top-level JSON object in document order, duplicates kept
/// (Go applies them in order, so the last one wins).
fn ordered_entries(data: &[u8]) -> Result<Vec<(String, Value)>, serde_json::Error> {
    use serde::de::{Deserializer as _, MapAccess, Visitor};
    struct Members;
    impl<'de> Visitor<'de> for Members {
        type Value = Vec<(String, Value)>;
        fn expecting(&self, formatter: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
            formatter.write_str("a JSON object")
        }
        fn visit_map<A: MapAccess<'de>>(self, mut map: A) -> Result<Self::Value, A::Error> {
            let mut entries = Vec::new();
            while let Some(entry) = map.next_entry::<String, Value>()? {
                entries.push(entry);
            }
            Ok(entries)
        }
        fn visit_unit<E>(self) -> Result<Self::Value, E> {
            Ok(Vec::new())
        }
    }
    let mut deserializer = serde_json::Deserializer::from_slice(data);
    let entries = deserializer.deserialize_any(Members)?;
    deserializer.end()?;
    Ok(entries)
}

/// Go `strconv.ParseBool` for a bool flag value.
fn parse_bool(text: &str) -> Option<bool> {
    match text {
        "1" | "t" | "T" | "true" | "TRUE" | "True" => Some(true),
        "0" | "f" | "F" | "false" | "FALSE" | "False" => Some(false),
        _ => None,
    }
}

fn last(matches: &ArgMatches, name: &str) -> Option<String> {
    matches
        .get_many::<String>(name)
        .and_then(|values| values.last().cloned())
}

fn changed(matches: &ArgMatches, name: &str) -> bool {
    matches.value_source(name) == Some(ValueSource::CommandLine)
}

fn environment(name: &str) -> Result<String, String> {
    Ok(symdesk_core::config::environment_value(name)?.unwrap_or_default())
}

fn environment_or(name: &str, fallback: &str) -> Result<String, String> {
    let value = environment(name)?;
    Ok(if value.is_empty() {
        fallback.to_owned()
    } else {
        value
    })
}

/// Go `portFromListen`.
fn port_from_listen(listen: &str) -> &str {
    listen
        .rfind(':')
        .map_or("8787", |index| &listen[index + 1..])
}

struct Settings {
    listen: String,
    token: String,
    worker_token: String,
    tls_cert: String,
    tls_key: String,
    local_worker: bool,
    worker_engine: String,
    ollama_url: String,
    ollama_model: String,
    language: String,
    vault: Option<String>,
}

fn settings(
    matches: &ArgMatches,
    vault_flag: Option<String>,
    options: &HomeAssistantOptions,
) -> Result<Settings, String> {
    let flag_or_env = |name: &str, env: &str| -> Result<String, String> {
        match last(matches, name) {
            Some(value) => Ok(value),
            None => environment(env),
        }
    };
    let mut token = flag_or_env("token", "SYMDESK_SERVER_TOKEN")?;
    if token.is_empty() {
        token.clone_from(&options.server_token);
    }
    let mut worker_token = flag_or_env("worker-token", "SYMDESK_WORKER_TOKEN")?;
    if worker_token.is_empty() {
        worker_token.clone_from(&options.worker_token);
    }
    // An options value replaces the flag's environment default, never a flag.
    let with_options = |name: &str, default: String, option: &str| -> String {
        if !option.is_empty() && !changed(matches, name) {
            option.to_owned()
        } else {
            last(matches, name).unwrap_or(default)
        }
    };
    let listen = with_options(
        "listen",
        environment_or("SYMDESK_SERVER_LISTEN", "127.0.0.1:8787")?,
        &options.listen,
    );
    let worker_engine = with_options(
        "worker-engine",
        environment_or("SYMDESK_WORKER_ENGINE", "tesseract")?,
        &options.worker_engine,
    );
    let ollama_url = with_options(
        "ollama-url",
        environment_or("OLLAMA_HOST", "http://127.0.0.1:11434")?,
        &options.ollama_url,
    );
    let ollama_model = with_options(
        "ollama-model",
        environment("SYMDESK_OLLAMA_MODEL")?,
        &options.ollama_model,
    );
    let local_worker = if changed(matches, "local-worker") {
        let text = last(matches, "local-worker").unwrap_or_default();
        parse_bool(&text).ok_or_else(|| {
            format!(
                "invalid argument {text:?} for \"--local-worker\" flag: strconv.ParseBool: parsing {text:?}: invalid syntax"
            )
        })?
    } else {
        options.local_processing
    };
    Ok(Settings {
        listen,
        token,
        worker_token,
        tls_cert: flag_or_env("tls-cert", "SYMDESK_TLS_CERT")?,
        tls_key: flag_or_env("tls-key", "SYMDESK_TLS_KEY")?,
        local_worker,
        worker_engine,
        ollama_url,
        ollama_model,
        language: flag_or_env_or(matches, "ocr-language", "SYMDESK_OCR_LANGUAGE", "deu+eng")?,
        vault: vault_flag,
    })
}

fn flag_or_env_or(
    matches: &ArgMatches,
    name: &str,
    env: &str,
    fallback: &str,
) -> Result<String, String> {
    match last(matches, name) {
        Some(value) => Ok(value),
        None => environment_or(env, fallback),
    }
}

fn fail(message: &str) -> ExitCode {
    crate::write_stderr(&format!("{message}\n"), CoreExitCode::Generic)
}

pub(crate) fn run(matches: &ArgMatches, vault_flag: Option<String>) -> ExitCode {
    let options_path = OPTIONS_PATH_OVERRIDE
        .get()
        .map_or_else(|| PathBuf::from(OPTIONS_PATH), Clone::clone);
    let options = read_options(&options_path);
    let settings = match settings(matches, vault_flag, &options) {
        Ok(settings) => settings,
        Err(error) => return fail(&error),
    };
    if settings.local_worker && !settings.tls_cert.is_empty() {
        return fail(
            "the built-in local worker cannot connect to a custom TLS listener; run it as a separate worker",
        );
    }
    // Go resolves an options vault only when neither the flag nor the
    // environment/config names one.
    let vault = match crate::resolve_vault_with_fallback(
        settings.vault.as_deref(),
        (!options.vault.is_empty()).then_some(options.vault.as_str()),
    ) {
        Ok(path) => path,
        Err(error) => return fail(&error),
    };
    let worker_token = (!settings.worker_token.is_empty()).then(|| settings.worker_token.clone());
    let prepared = match http::prepare(http::HttpConfig {
        listen_address: settings.listen.clone(),
        vault_root: vault,
        token: settings.token.clone(),
        worker_token,
        version: crate::VERSION.to_owned(),
        tls_cert: settings.tls_cert.clone(),
        tls_key: settings.tls_key.clone(),
    }) {
        Ok(prepared) => prepared,
        Err(error) => return fail(&error),
    };
    let local_worker = if settings.local_worker {
        let token = if settings.worker_token.is_empty() {
            &settings.token
        } else {
            &settings.worker_token
        };
        match worker_cli::spawn_local(worker_cli::LocalWorkerSettings {
            server_url: format!("http://127.0.0.1:{}", port_from_listen(&settings.listen)),
            token: token.clone(),
            engine: settings.worker_engine.clone(),
            ollama_url: settings.ollama_url.clone(),
            ollama_model: settings.ollama_model.clone(),
            ocr_language: settings.language.clone(),
        }) {
            Ok(worker) => Some(worker),
            Err(error) => return fail(&error),
        }
    } else {
        None
    };
    let runtime = match tokio::runtime::Builder::new_multi_thread()
        .enable_all()
        .build()
    {
        Ok(runtime) => runtime,
        Err(error) => return fail(&format!("create runtime: {error}")),
    };
    let result = runtime.block_on(prepared.serve());
    if let Some(worker) = local_worker {
        worker.stop();
    }
    match result {
        Ok(()) => crate::process_exit(CoreExitCode::Ok),
        Err(error) => fail(&error),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn options_follow_go_json_decoding() {
        let options = parse_options(
            br#"{"Server_Token":"a","listen":"x","listen":"y","local_processing":"yes","vault":7,"worker_engine":null,"ollama_url":"u"}"#,
        );
        assert_eq!(options.server_token, "a");
        assert_eq!(options.listen, "y");
        assert!(!options.local_processing);
        assert_eq!(options.vault, "");
        assert_eq!(options.worker_engine, "");
        assert_eq!(options.ollama_url, "u");
        assert_eq!(parse_options(b"{not json"), HomeAssistantOptions::default());
        assert_eq!(parse_options(b"[1]"), HomeAssistantOptions::default());
        assert_eq!(parse_options(b"null"), HomeAssistantOptions::default());
    }

    #[test]
    fn port_comes_from_the_last_colon() {
        assert_eq!(port_from_listen("127.0.0.1:9000"), "9000");
        assert_eq!(port_from_listen(":80"), "80");
        assert_eq!(port_from_listen("nohost"), "8787");
    }

    #[test]
    fn go_bool_syntax() {
        assert_eq!(parse_bool("T"), Some(true));
        assert_eq!(parse_bool("False"), Some(false));
        assert_eq!(parse_bool("yes"), None);
    }
}
