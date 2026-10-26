//! Cobra's generated documents are release assets, captured from the real Go
//! process for every node of the frozen tree. They are not Clap templates.
use std::{collections::BTreeMap, ffi::OsString, process::ExitCode, sync::OnceLock};

use serde::Deserialize;
use symaira_core_exit::ExitCode as CoreExitCode;

#[derive(Deserialize)]
struct Documents {
    help: BTreeMap<String, String>,
    completion: BTreeMap<String, String>,
    root_usage: String,
}

fn documents() -> &'static Documents {
    static DOCUMENTS: OnceLock<Documents> = OnceLock::new();
    DOCUMENTS.get_or_init(|| {
        serde_json::from_str(include_str!("../assets/cobra-help.json"))
            .expect("Go-generated Cobra documents")
    })
}

pub fn render(path: &str) -> ExitCode {
    crate::write_stdout(documents().help[path].clone())
}

/// Handle documentation before Clap, including help for not-yet-native commands.
/// Never initialize a vault or read configuration for help, as Cobra does not.
pub fn intercept(args: &[OsString]) -> Option<ExitCode> {
    let args: Vec<&str> = args
        .iter()
        .skip(1)
        .map(|s| s.to_str())
        .collect::<Option<_>>()?;
    let mut words = Vec::new();
    let mut help = false;
    let mut no_descriptions = false;
    let mut output = "";
    let mut index = 0;
    while index < args.len() {
        let arg = args[index];
        match arg {
            "--" => {
                words.extend_from_slice(&args[index + 1..]);
                break;
            }
            "--help" | "-h" | "--help=true" => help = true,
            "--help=false" => help = false,
            "--no-descriptions" | "--no-descriptions=true" => no_descriptions = true,
            "--no-descriptions=false" => no_descriptions = false,
            "--output" | "--vault" => {
                index += 1;
                let value = *args.get(index)?;
                if arg == "--output" {
                    output = value;
                }
            }
            "--json" | "--json=true" | "--json=false" => {}
            _ if arg.starts_with("--output=") => output = &arg[9..],
            _ if arg.starts_with("--vault=") => {}
            // Command-local flags may precede --help. Their values cannot
            // become command names: use the longest known command prefix.
            _ => words.push(arg),
        }
        index += 1;
    }
    let docs = documents();
    let help_command = !help && words.first() == Some(&"help");
    if help_command {
        words.remove(0);
    }
    if help || help_command || words.is_empty() {
        let mut path = String::new();
        for word in &words {
            let candidate = if path.is_empty() {
                (*word).to_owned()
            } else {
                format!("{path} {word}")
            };
            if docs.help.contains_key(&candidate) {
                path = candidate;
            } else if help_command {
                let topics = words
                    .iter()
                    .map(|s| format!("`{s}`"))
                    .collect::<Vec<_>>()
                    .join(" ");
                return Some(crate::write_stderr(
                    &format!("Unknown help topic [{topics}]\n{}", docs.root_usage),
                    CoreExitCode::Ok,
                ));
            } else {
                break;
            }
        }
        return Some(crate::write_stdout(docs.help.get(&path)?.clone()));
    }
    if words.first() == Some(&"completion") && words.len() == 2 {
        let mut key = words[1].to_owned();
        if no_descriptions {
            key.push_str("-no-descriptions");
        }
        let script = docs.completion.get(&key)?;
        if let Err(error) = crate::config_paths::load_root_config() {
            return Some(crate::write_stderr(
                &format!("failed to load config: {error}\n"),
                CoreExitCode::Config,
            ));
        }
        if !output.is_empty() && !matches!(output, "text" | "json" | "yaml") {
            return Some(crate::write_stderr(
                &format!("invalid --output value {output:?} (want text|json|yaml)\n"),
                CoreExitCode::Generic,
            ));
        }
        return Some(crate::write_stdout(script.clone()));
    }
    None
}
