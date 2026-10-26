//! Native configuration commands; storage and precedence use the shared loader.
use serde_json::{Value, json};
use std::{collections::BTreeMap, ffi::OsString, fs, process::ExitCode};
use symaira_core_exit::ExitCode as CoreExitCode;
use symdesk_core::config::{self, Config};
use symdesk_vault::go_quote;

const PUBLIC_KEYS: &[&str] = &[
    "agent_max_iterations",
    "history_max_age_days",
    "history_max_per_file",
    "inbox",
    "language",
    "llm_model",
    "llm_provider",
    "max_tokens",
    "ollama_url",
    "results_max_age_days",
    "results_max_per_task",
    "review_threshold",
    "storage_path_template",
    "trash_retention_days",
    "vault",
];

#[derive(Default)]
struct Arguments {
    words: Vec<String>,
    flags: BTreeMap<String, String>,
    json: bool,
    output: String,
    vault: String,
}

fn boolean(value: &str) -> Option<bool> {
    match value {
        "1" | "t" | "T" | "true" | "TRUE" | "True" => Some(true),
        "0" | "f" | "F" | "false" | "FALSE" | "False" => Some(false),
        _ => None,
    }
}

fn parse(args: &[String], parsed: &mut Arguments) -> Result<(), String> {
    let mut index = 0;
    while index < args.len() {
        let arg = &args[index];
        if arg == "--" {
            parsed.words.extend_from_slice(&args[index + 1..]);
            break;
        }
        if let Some(flag) = arg.strip_prefix("--") {
            let (name, inline) = flag
                .split_once('=')
                .map_or((flag, None), |(n, v)| (n, Some(v)));
            if name == "json" {
                let value = inline.unwrap_or("true");
                parsed.json = boolean(value).ok_or_else(|| format!("invalid argument {} for \"--json\" flag: strconv.ParseBool: parsing {}: invalid syntax", go_quote(value), go_quote(value)))?;
            } else {
                if !matches!(
                    name,
                    "output"
                        | "vault"
                        | "provider"
                        | "ollama-url"
                        | "model"
                        | "api-key"
                        | "max-tokens"
                ) || (parsed.words.first().is_some_and(|s| s == "config")
                    && !matches!(name, "output" | "vault"))
                {
                    return Err(format!("unknown flag: --{name}"));
                }
                let value = match inline {
                    Some(value) => value.to_owned(),
                    None => {
                        index += 1;
                        args.get(index)
                            .cloned()
                            .ok_or_else(|| format!("flag needs an argument: --{name}"))?
                    }
                };
                match name {
                    "output" => parsed.output = value,
                    "vault" => parsed.vault = value,
                    _ => {
                        if parsed.words.get(1).map(String::as_str) != Some("set") {
                            return Err(format!("unknown flag: --{name}"));
                        }
                        if name == "max-tokens" {
                            parse_flag_int(&value).map_err(|reason| format!("invalid argument {} for \"--max-tokens\" flag: strconv.ParseInt: parsing {}: {reason}",go_quote(&value),go_quote(&value)))?;
                        }
                        parsed.flags.insert(name.to_owned(), value);
                    }
                }
            }
        } else if arg.starts_with('-') && arg != "-" {
            let c = arg.chars().nth(1).unwrap_or('-');
            return Err(format!("unknown shorthand flag: '{c}' in {arg}"));
        } else {
            parsed.words.push(arg.clone());
        }
        index += 1;
    }
    Ok(())
}

// pflag integers use Go's base-zero strconv parser, not decimal config setters.
fn parse_flag_int(value: &str) -> Result<i64, &'static str> {
    crate::duplicates_cli::parse_go_int(value)
}

pub fn intercept(args: &[OsString]) -> Option<ExitCode> {
    let args: Vec<String> = args
        .iter()
        .skip(1)
        .map(|s| s.to_str().map(str::to_owned))
        .collect::<Option<_>>()?;
    let mut index = 0;
    while index < args.len() && args[index].starts_with('-') {
        if matches!(args[index].as_str(), "--vault" | "--output") {
            index += 1;
        }
        index += 1;
    }
    let group = args.get(index)?.as_str();
    if !matches!(group, "config" | "ai-config")
        || (group == "config" && args.get(index + 1).is_some_and(|s| s == "paths"))
    {
        return None;
    }
    let mut parsed = Arguments::default();
    if let Err(error) = parse(&args, &mut parsed) {
        return Some(crate::emit_error(error, parsed.json));
    }
    if parsed.words.len() == 1 {
        return Some(crate::help_cli::render(group));
    }
    let (mut loaded, environment) = match crate::config_paths::load_root_config() {
        Ok(value) => value,
        Err(error) => {
            return Some(crate::write_stderr(
                &format!("failed to load config: {error}\n"),
                CoreExitCode::Config,
            ));
        }
    };
    // Cobra argument validators use raw --json before output resolution.
    if group == "config" && parsed.words[1] == "set" && parsed.words.len() != 4 {
        return Some(crate::emit_error(
            format!("accepts 2 arg(s), received {}", parsed.words.len() - 2),
            parsed.json,
        ));
    }
    if !parsed.output.is_empty() && !matches!(parsed.output.as_str(), "text" | "json" | "yaml") {
        return Some(crate::write_stderr(
            &format!(
                "invalid --output value {} (want text|json|yaml)\n",
                go_quote(&parsed.output)
            ),
            CoreExitCode::Generic,
        ));
    }
    let json = match parsed.output.as_str() {
        "json" => true,
        "text" | "yaml" => false,
        _ => parsed.json,
    };
    let path = config::global_path(&environment);
    let result = if group == "config" {
        run_config(&parsed, &loaded, &path)
    } else {
        if !parsed.vault.is_empty() {
            loaded.vault = parsed.vault.clone();
        }
        run_ai(&parsed, &mut loaded, &path, json)
    };
    Some(match result {
        Ok(text) => crate::write_stdout(text),
        Err(error) => crate::emit_error(error, json),
    })
}

fn string_field(value: &Value) -> String {
    value
        .as_str()
        .map(str::to_owned)
        .unwrap_or_else(|| value.to_string())
}

fn run_config(args: &Arguments, loaded: &Config, path: &str) -> Result<String, String> {
    let operation = &args.words[1];
    let extra = &args.words[2..];
    match operation.as_str() {
        "path" => Ok(format!("{path}\n")),
        "get" => {
            let value = serde_json::to_value(loaded).map_err(|e| e.to_string())?;
            if extra.len() > 1 {
                return Err("usage: config get [key]".to_owned());
            }
            if let Some(key) = extra.first() {
                if !PUBLIC_KEYS.contains(&key.as_str()) {
                    return Err(format!("unknown config key {}", go_quote(key)));
                }
                return Ok(format!("{}\n", string_field(&value[key])));
            }
            Ok(PUBLIC_KEYS
                .iter()
                .map(|key| format!("{key} = {}\n", string_field(&value[key])))
                .collect())
        }
        "set" => {
            let key = &extra[0];
            let text = &extra[1];
            if !PUBLIC_KEYS.contains(&key.as_str()) {
                return Err(format!("unknown config key {}", go_quote(key)));
            }
            let mut value = serde_json::to_value(loaded).map_err(|e| e.to_string())?;
            value[key] = if value[key].is_number() {
                let integer = text
                    .parse::<i64>()
                    .map_err(|_| format!("unknown config key {}", go_quote(key)))?;
                json!(integer)
            } else {
                json!(text)
            };
            let cfg: Config = serde_json::from_value(value).map_err(|e| e.to_string())?;
            config::save(path, &cfg).map_err(|e| format!("failed to save config: {e}"))?;
            Ok(format!("set {key} = {text} in {path}\n"))
        }
        "init" => {
            if fs::metadata(path).is_ok() {
                return Ok(format!("config already exists at {path}\n"));
            }
            config::save(path, &Config::default())
                .map_err(|e| format!("failed to write config: {e}"))?;
            Ok(format!("wrote default config to {path}\n"))
        }
        _ => Err(format!(
            "unknown command {} for \"symdesk config\"",
            go_quote(operation)
        )),
    }
}

fn run_ai(
    args: &Arguments,
    cfg: &mut Config,
    path: &str,
    json_output: bool,
) -> Result<String, String> {
    let provider = if cfg.llm_provider.is_empty() {
        "ollama"
    } else {
        &cfg.llm_provider
    };
    match args.words[1].as_str() {
        "show" => {
            if json_output {
                return encode(
                    json!({"provider":provider,"ollama_url":cfg.ollama_url,"model":cfg.llm_model,"max_tokens":cfg.max_tokens,"has_api_key":cfg.has_api_key()}),
                );
            }
            Ok(format!(
                "Provider: {provider}\nOllama URL: {}\nModel: {}\nMax tokens: {}\nAPI key configured: {}\n",
                cfg.ollama_url,
                cfg.llm_model,
                cfg.max_tokens,
                cfg.has_api_key()
            ))
        }
        "set" => {
            let mut value = serde_json::to_value(&*cfg).map_err(|e| e.to_string())?;
            for (flag, field) in [
                ("provider", "llm_provider"),
                ("ollama-url", "ollama_url"),
                ("model", "llm_model"),
                ("api-key", "llm_api_key"),
                ("max-tokens", "max_tokens"),
            ] {
                if let Some(text) = args.flags.get(flag) {
                    value[field] = if flag == "max-tokens" {
                        json!(parse_flag_int(text).map_err(str::to_owned)?)
                    } else {
                        json!(text)
                    };
                }
            }
            *cfg = serde_json::from_value(value).map_err(|e| e.to_string())?;
            config::save(path, cfg).map_err(|e| format!("failed to save config: {e}"))?;
            Ok(if json_output {
                "{\"status\":\"ok\"}\n"
            } else {
                "AI configuration saved.\n"
            }
            .to_owned())
        }
        "test" => {
            if provider == "anthropic" {
                let key = crate::ai_secrets::resolve_key(cfg.api_key_reference());
                let ok = !key.is_empty();
                let error = "Anthropic API key could not be resolved (missing secret via symvault or environment variable).";
                if json_output {
                    return encode(if ok {
                        json!({"provider":provider,"ok":true})
                    } else {
                        json!({"provider":provider,"ok":false,"error":error})
                    });
                }
                return Ok(if ok {
                    "Connection OK.\n".to_owned()
                } else {
                    format!("Connection failed: {error}\n")
                });
            }
            if cfg.ollama_url.is_empty() {
                let error = "Ollama URL is not set.";
                return if json_output {
                    encode(json!({"provider":provider,"ok":false,"error":error}))
                } else {
                    Ok(format!("Connection failed: {error}\n"))
                };
            }
            let descriptor = symaira_core_llm::lookup("ollama").expect("embedded Ollama provider");
            let probe = symaira_core_llm::ClientBuilder::new(descriptor.clone(), "")
                .base_url(&cfg.ollama_url)
                .timeout(std::time::Duration::from_secs(10))
                .build();
            let mut result = json!({"provider":provider,"ok":false});
            match probe {
                Err(error) => result["error"] = json!(error.to_string()),
                Ok(client) => match client.ping() {
                    Err(error) => result["error"] = json!(error.to_string()),
                    Ok(()) => {
                        result["ok"] = json!(true);
                        match client.list_models() {
                            Ok(models) => {
                                result["models"] =
                                    json!(models.iter().map(|m| m.id.clone()).collect::<Vec<_>>())
                            }
                            Err(error) => {
                                result["models"] = json!([]);
                                result["models_error"] = json!(error.to_string());
                            }
                        }
                    }
                },
            }
            if json_output {
                return encode(result);
            }
            if result["ok"] == true {
                let mut text = "Connection OK.\n".to_owned();
                if let Some(models) = result["models"].as_array().filter(|m| !m.is_empty()) {
                    text.push_str(&format!(
                        "Models: [{}]\n",
                        models
                            .iter()
                            .filter_map(Value::as_str)
                            .collect::<Vec<_>>()
                            .join(" ")
                    ));
                }
                Ok(text)
            } else {
                Ok(format!(
                    "Connection failed: {}\n",
                    result["error"].as_str().unwrap_or_default()
                ))
            }
        }
        operation => Err(format!(
            "unknown command {} for \"symdesk ai-config\"",
            go_quote(operation)
        )),
    }
}

fn encode(value: Value) -> Result<String, String> {
    // Go encodes these maps with sorted keys. Sort explicitly: workspace
    // feature unification may enable serde_json `preserve_order`.
    serde_json::to_string(&sort_keys(value))
        .map(|s| format!("{}\n", crate::go_escape_json(s)))
        .map_err(|e| e.to_string())
}

pub(crate) fn sort_keys(value: Value) -> Value {
    match value {
        Value::Object(map) => {
            let mut entries: Vec<_> = map.into_iter().collect();
            entries.sort_by(|left, right| left.0.cmp(&right.0));
            Value::Object(
                entries
                    .into_iter()
                    .map(|(key, value)| (key, sort_keys(value)))
                    .collect(),
            )
        }
        Value::Array(items) => Value::Array(items.into_iter().map(sort_keys).collect()),
        other => other,
    }
}
