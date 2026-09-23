#![deny(unsafe_code)]

use std::{
    ffi::OsString,
    io::{self, Write},
    path::PathBuf,
    process::ExitCode,
};

use symaira_core_exit::ExitCode as CoreExitCode;
use symroom_core::{artifact, identity};

const USAGE: &str = "Usage: symroom artifact <link|unlink|list> [flags] [args]\n";
const LINK_FLAGS: &str = "Usage of artifact link:\n  -identity string\n    \tAuthor identity name\n  -title string\n    \tArtifact title\n";
const UNLINK_FLAGS: &str =
    "Usage of artifact unlink:\n  -identity string\n    \tAuthor identity name\n";
const LIST_FLAGS: &str = "Usage of artifact list:\n  -json\n    \tOutput artifacts as JSON\n";

pub fn run(args: &[OsString]) -> ExitCode {
    let Some(action) = args.first() else {
        return stdout(USAGE, CoreExitCode::Ok);
    };
    match action.to_string_lossy().as_ref() {
        "link" => link(&args[1..]),
        "unlink" => unlink(&args[1..]),
        "list" => list(&args[1..]),
        other => stderr(
            &format!("Unknown artifact action: {other}\n"),
            CoreExitCode::NoInput,
        ),
    }
}

fn link(args: &[OsString]) -> ExitCode {
    let parsed = match parse_flags(args, &["identity", "title"], &[], LINK_FLAGS) {
        Ok(parsed) => parsed,
        Err(code) => return code,
    };
    let Some(path) = parsed.positionals.first() else {
        return stderr(
            "Usage: symroom artifact link <path> [--title ...] [--identity <name>]\n",
            CoreExitCode::NoInput,
        );
    };
    let signer = match load_identity(parsed.values.get("identity")) {
        Ok(signer) => signer,
        Err(code) => return code,
    };
    match artifact::link(
        &room_dir(),
        &PathBuf::from(path),
        parsed.values.get("title").map_or("", String::as_str),
        &signer,
    ) {
        Ok(event) => stdout(&format!("{}\n", event.id), CoreExitCode::Ok),
        Err(artifact::ArtifactError::OutsideRoot) => stderr(
            "Error: path is outside artifact root\n",
            CoreExitCode::NoInput,
        ),
        Err(error) => stderr(
            &format!("Error linking artifact: {error}\n"),
            CoreExitCode::Generic,
        ),
    }
}

fn unlink(args: &[OsString]) -> ExitCode {
    let parsed = match parse_flags(args, &["identity"], &[], UNLINK_FLAGS) {
        Ok(parsed) => parsed,
        Err(code) => return code,
    };
    let Some(artifact_id) = parsed.positionals.first() else {
        return stderr(
            "Usage: symroom artifact unlink <artifact_id> [--identity <name>]\n",
            CoreExitCode::NoInput,
        );
    };
    let signer = match load_identity(parsed.values.get("identity")) {
        Ok(signer) => signer,
        Err(code) => return code,
    };
    match artifact::unlink(&room_dir(), artifact_id, &signer) {
        Ok(event) => stdout(&format!("{}\n", event.id), CoreExitCode::Ok),
        Err(error) => stderr(
            &format!("Error unlinking artifact: {error}\n"),
            CoreExitCode::Generic,
        ),
    }
}

fn list(args: &[OsString]) -> ExitCode {
    let parsed = match parse_flags(args, &[], &["json"], LIST_FLAGS) {
        Ok(parsed) => parsed,
        Err(code) => return code,
    };
    let json = parsed
        .values
        .get("json")
        .is_some_and(|value| parse_bool(value).unwrap_or(false));
    let artifacts = match artifact::list(&room_dir()) {
        Ok(artifacts) => artifacts,
        Err(error) => {
            return stderr(
                &format!("Error listing artifacts: {error}\n"),
                CoreExitCode::Generic,
            );
        }
    };
    if json {
        let output = if artifacts.is_empty() {
            "null".to_owned()
        } else {
            go_json(&artifacts)
        };
        return stdout(&format!("{output}\n"), CoreExitCode::Ok);
    }
    let output = artifacts
        .iter()
        .map(|artifact| {
            format!(
                "{}\t{}\t[{}]\t{}\n",
                artifact.id, artifact.path, artifact.status, artifact.title
            )
        })
        .collect::<String>();
    stdout(&output, CoreExitCode::Ok)
}

#[derive(Default)]
struct Parsed {
    values: std::collections::BTreeMap<String, String>,
    positionals: Vec<String>,
}

fn parse_flags(
    args: &[OsString],
    values: &[&str],
    booleans: &[&str],
    usage: &str,
) -> Result<Parsed, ExitCode> {
    let mut parsed = Parsed::default();
    let mut index = 0;
    while index < args.len() {
        let argument = args[index].to_string_lossy();
        if argument == "--" {
            parsed.positionals.extend(
                args[index + 1..]
                    .iter()
                    .map(|arg| arg.to_string_lossy().into_owned()),
            );
            break;
        }
        if !argument.starts_with('-') || argument == "-" {
            parsed.positionals.extend(
                args[index..]
                    .iter()
                    .map(|arg| arg.to_string_lossy().into_owned()),
            );
            break;
        }
        let flag = argument.trim_start_matches('-');
        let (name, inline) = flag
            .split_once('=')
            .map_or((flag, None), |(name, value)| (name, Some(value.to_owned())));
        if matches!(name, "h" | "help") && inline.is_none() {
            return Err(stderr(usage, CoreExitCode::Ok));
        }
        if values.contains(&name) {
            let value = if let Some(value) = inline {
                value
            } else if let Some(value) = args.get(index + 1) {
                index += 1;
                value.to_string_lossy().into_owned()
            } else {
                return Err(stderr(
                    &format!("flag needs an argument: -{name}\n{usage}"),
                    CoreExitCode::NoInput,
                ));
            };
            parsed.values.insert(name.to_owned(), value);
            index += 1;
            continue;
        }
        if booleans.contains(&name) {
            let value = match inline.as_deref() {
                None => "true",
                Some(value) if parse_bool(value).is_some() => value,
                Some(value) => {
                    return Err(stderr(
                        &format!(
                            "invalid boolean value \"{value}\" for -{name}: parse error\n{usage}"
                        ),
                        CoreExitCode::NoInput,
                    ));
                }
            };
            parsed.values.insert(name.to_owned(), value.to_owned());
            index += 1;
            continue;
        }
        return Err(stderr(
            &format!("flag provided but not defined: -{name}\n{usage}"),
            CoreExitCode::NoInput,
        ));
    }
    Ok(parsed)
}

fn load_identity(name: Option<&String>) -> Result<identity::Identity, ExitCode> {
    let Some(name) = name.filter(|name| !name.is_empty()) else {
        return Err(stderr(
            "Error: --identity is required when default_identity is not configured\n",
            CoreExitCode::NoInput,
        ));
    };
    identity::load(name).map_err(|error| {
        stderr(
            &format!("Error loading identity {name}: {error}\n"),
            CoreExitCode::NotFound,
        )
    })
}

fn room_dir() -> PathBuf {
    std::env::var_os("SYMROOM_ROOM_DIR")
        .filter(|value| !value.is_empty())
        .map(PathBuf::from)
        .unwrap_or_else(|| PathBuf::from("."))
}

fn go_json(value: &impl serde::Serialize) -> String {
    serde_json::to_string_pretty(value)
        .unwrap_or_else(|_| "null".to_owned())
        .replace('&', "\\u0026")
        .replace('<', "\\u003c")
        .replace('>', "\\u003e")
        .replace('\u{2028}', "\\u2028")
        .replace('\u{2029}', "\\u2029")
}

fn parse_bool(value: &str) -> Option<bool> {
    match value {
        "1" | "t" | "T" | "TRUE" | "True" | "true" => Some(true),
        "0" | "f" | "F" | "FALSE" | "False" | "false" => Some(false),
        _ => None,
    }
}

fn stdout(value: &str, code: CoreExitCode) -> ExitCode {
    if io::stdout().write_all(value.as_bytes()).is_err() {
        process_exit(CoreExitCode::Generic)
    } else {
        process_exit(code)
    }
}

fn stderr(value: &str, code: CoreExitCode) -> ExitCode {
    if io::stderr().write_all(value.as_bytes()).is_err() {
        process_exit(CoreExitCode::Generic)
    } else {
        process_exit(code)
    }
}

fn process_exit(code: CoreExitCode) -> ExitCode {
    ExitCode::from(code.as_u8())
}
