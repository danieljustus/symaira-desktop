//! Indexed link inspection commands.
use crate::{emit_error, relative_path, resolve_vault, write_go_json, write_stdout};
use clap::{Arg, ArgMatches, Command};
use std::{fs, process::ExitCode};
use symdesk_index::open_for_vault;
use symdesk_vault::{go_quote, secure_path};

/// Normalize only this command's pflag-compatible inherited flags. Other
/// command families retain their existing parser paths.
pub fn prepare_args(args: &mut Vec<std::ffi::OsString>) -> Option<ExitCode> {
    let mut index = 1;
    let mut selected = false;
    let mut family = String::new();
    while index < args.len() {
        let token = args[index].to_str()?;
        if token == "--" {
            return None;
        }
        if token == "--vault" || token == "--output" {
            index += 2;
            continue;
        }
        if token.starts_with('-') {
            index += 1;
            continue;
        }
        family = token.to_owned();
        selected = matches!(
            token,
            "backlinks" | "graph" | "history" | "trash" | "conflict"
        );
        if token == "relations" {
            index += 1;
            while index < args.len() {
                let nested = args[index].to_str()?;
                if nested == "--vault" || nested == "--output" {
                    index += 2;
                    continue;
                }
                if nested == "--" {
                    break;
                }
                if nested.starts_with('-') {
                    index += 1;
                    continue;
                }
                selected = nested == "inverse";
                break;
            }
        }
        break;
    }
    if !selected {
        return None;
    }
    let mut normalized = vec![args[0].clone()];
    let mut output = None;
    let mut vault = None;
    let mut json = false;
    let mut all = false;
    index = 1;
    while index < args.len() {
        let token = args[index].to_str()?;
        if token == "--" {
            normalized.extend_from_slice(&args[index..]);
            break;
        }
        if token == "--json" {
            json = true;
            index += 1;
            continue;
        }
        if let Some(raw) = token.strip_prefix("--json=") {
            json = match raw {
                "1" | "t" | "T" | "true" | "TRUE" | "True" => true,
                "0" | "f" | "F" | "false" | "FALSE" | "False" => false,
                _ => {
                    return Some(emit_error(
                        format!(
                            "invalid argument {} for \"--json\" flag: strconv.ParseBool: parsing {}: invalid syntax",
                            go_quote(raw),
                            go_quote(raw)
                        ),
                        json,
                    ));
                }
            };
            index += 1;
            continue;
        }
        let flag = token.split_once('=').map_or(token, |(flag, _)| flag);
        if flag == "--vault" || flag == "--output" {
            let value = if let Some((_, value)) = token.split_once('=') {
                value.to_owned()
            } else {
                index += 1;
                let Some(value) = args.get(index) else {
                    return Some(emit_error(format!("flag needs an argument: {flag}"), json));
                };
                value.to_str()?.to_owned()
            };
            if flag == "--vault" {
                vault = Some(value);
            } else {
                output = Some(value);
            }
        } else if (family == "history"
            && matches!(
                flag,
                "--max-per-file" | "--max-age-days" | "--checkpoint-age-days"
            ))
            || (family == "trash" && flag == "--older-than-days")
        {
            let raw = if let Some((_, value)) = token.split_once('=') {
                value
            } else {
                index += 1;
                let Some(value) = args.get(index) else {
                    return Some(emit_error(format!("flag needs an argument: {flag}"), json));
                };
                value.to_str()?
            };
            match crate::duplicates_cli::parse_go_int(raw) {
                Ok(value) => normalized.push(format!("{flag}={value}").into()),
                Err(reason) => {
                    return Some(emit_error(
                        format!(
                            "invalid argument {} for {} flag: strconv.ParseInt: parsing {}: {reason}",
                            go_quote(raw),
                            go_quote(flag),
                            go_quote(raw)
                        ),
                        json,
                    ));
                }
            }
        } else if family == "conflict" && flag == "--action" {
            normalized.push(args[index].clone());
            if !token.contains('=') {
                index += 1;
                let Some(value) = args.get(index) else {
                    return Some(emit_error(format!("flag needs an argument: {flag}"), json));
                };
                normalized.push(value.clone());
            }
        } else if family == "trash" && flag == "--all" {
            let raw = token.split_once('=').map_or("true", |(_, value)| value);
            all = match raw {
                "1" | "t" | "T" | "true" | "TRUE" | "True" => true,
                "0" | "f" | "F" | "false" | "FALSE" | "False" => false,
                _ => {
                    return Some(emit_error(
                        format!(
                            "invalid argument {} for \"--all\" flag: strconv.ParseBool: parsing {}: invalid syntax",
                            go_quote(raw),
                            go_quote(raw)
                        ),
                        json,
                    ));
                }
            };
        } else if token.starts_with("--") && token != "--help" {
            return Some(emit_error(format!("unknown flag: {flag}"), json));
        } else {
            normalized.push(args[index].clone());
        }
        index += 1;
    }
    // Insert inherited flags before `--`, never behind the positional sentinel.
    let at = normalized
        .iter()
        .position(|v| v == "--")
        .unwrap_or(normalized.len());
    let mut globals = Vec::new();
    if all {
        globals.push(std::ffi::OsString::from("--all"));
    }
    if json {
        globals.push(std::ffi::OsString::from("--json"));
    }
    for (name, value) in [("--output", output), ("--vault", vault)] {
        if let Some(value) = value {
            globals.extend([name.into(), value.into()]);
        }
    }
    normalized.splice(at..at, globals);
    *args = normalized;
    None
}

pub fn backlinks_cli() -> Command {
    Command::new("backlinks")
        .about("Get backlinks for a file or opaque contact reference target")
        .arg(Arg::new("file").num_args(0..))
}

pub fn run_backlinks(command: &ArgMatches, vault: Option<&str>, output_json: bool) -> ExitCode {
    let files: Vec<_> = command
        .get_many::<String>("file")
        .into_iter()
        .flatten()
        .collect();
    if files.len() != 1 {
        return emit_error(
            format!("accepts 1 arg(s), received {}", files.len()),
            output_json,
        );
    }
    let result = (|| -> Result<Vec<String>, String> {
        let root = resolve_vault(vault).map_err(|e| {
            if e.starts_with("vault path not configured") {
                "vault path not configured (use flag or SYMDESK_VAULT env)".to_owned()
            } else {
                crate::index_cli::index_vault_error(vault, &e)
            }
        })?;
        let mut sidecar = open_for_vault(&root).map_err(|e| e.to_string())?;
        let contact = files[0].strip_prefix("contact:").is_some_and(|rest| {
            let parts: Vec<_> = rest.split(':').collect();
            parts.len() == 3 && parts.iter().all(|p| !p.is_empty())
        });
        let target = if contact {
            files[0].to_owned()
        } else {
            secure_path(&root, files[0])
                .map_err(|e| e.to_string())?
                .to_string_lossy()
                .into_owned()
        };
        let result = sidecar
            .backlinks(&target, contact)
            .map_err(|e| e.to_string())
            .map(|links| {
                links
                    .into_iter()
                    .map(|p| {
                        let canonical = fs::canonicalize(&p)
                            .map(|p| p.to_string_lossy().into_owned())
                            .unwrap_or(p);
                        relative_path(&root, &canonical)
                    })
                    .collect()
            });
        if let Err(e) = sidecar.close() {
            eprintln!("warning: failed to close sidecar database: {e}");
        }
        result
    })();
    match result {
        Ok(links) if output_json => write_go_json(&links),
        Ok(links) => write_stdout(format!("[{}]\n", links.join(" "))),
        Err(e) => emit_error(e, output_json),
    }
}
