//! The Go CLI is a Cobra/pflag program. Its observable behaviour (which word is
//! a command, what a flag consumes, every parse diagnostic, `--help`,
//! `--version`, generated `help`/`completion`/`__complete`, argument counts,
//! required and mutually exclusive flags) is reproduced here from the recorded
//! command tree, ahead of the Clap pipeline that executes the commands.
mod complete;
mod find;
mod pflag;
mod quote;
mod tree;

use std::{ffi::OsString, process::ExitCode};

use symaira_core_exit::ExitCode as CoreExitCode;

use self::{
    find::find,
    pflag::State,
    quote::{go_json_string, go_quote, go_sharp_quote},
    tree::{Builtin, Tree},
};

/// What `main` does after Cobra's front half has run.
pub enum Front {
    /// Cobra already produced the complete result.
    Exit(ExitCode),
    /// A runnable command that Clap executes; the arguments are Cobra-valid.
    Continue(Vec<OsString>),
}

/// Go prints a returned error as JSON when `--json` was parsed, else to stderr.
fn report(message: &str, json: bool) -> ExitCode {
    if json {
        let line = format!("{{\"error\":{}}}\n", go_json_string(message));
        if crate::write_stdout(line) != crate::process_exit(CoreExitCode::Ok) {
            return crate::process_exit(CoreExitCode::Generic);
        }
        return crate::process_exit(CoreExitCode::Generic);
    }
    crate::write_stderr(&format!("{message}\n"), CoreExitCode::Generic)
}

fn words_of(args: &[OsString]) -> Option<Vec<String>> {
    args.iter()
        .skip(1)
        .map(|arg| arg.to_str().map(str::to_owned))
        .collect()
}

pub fn front(args: &[OsString]) -> Front {
    // Non-UTF-8 argument bytes cannot be named by the recorded grammar: leave
    // those invocations to the Clap pipeline.
    let Some(words) = words_of(args) else {
        return Front::Continue(args.to_vec());
    };
    let tree = Tree::get();
    let found = find(tree, &words, true);
    if let Some(message) = &found.error {
        return Front::Exit(report(message, false));
    }
    execute(tree, found, args)
}

fn execute(tree: &Tree, found: find::Found, original: &[OsString]) -> Front {
    let command = found.node;
    let called_as = found.called_as.as_str();
    let arguments = found.args.clone();
    let node = tree.node(command);
    let key = tree.help_key(command).to_owned();
    let mut json = false;
    let mut output = String::new();
    let positional;
    let state: Option<State>;
    if node.disable_flag_parsing {
        positional = arguments.clone();
        state = None;
    } else {
        let flags = tree.flag_set(command, true);
        let (parsed, result) = pflag::parse(&flags, &arguments);
        json = parsed.bool_value("json");
        if let Err(message) = result {
            return Front::Exit(report(&message, json));
        }
        if parsed.bool_value("help") {
            return Front::Exit(crate::help_cli::render(&key));
        }
        if command == tree.root && parsed.bool_value("version") {
            return Front::Exit(crate::write_stdout(format!(
                "symdesk version {}\n",
                crate::VERSION
            )));
        }
        positional = parsed.args.clone();
        // `export --output FILE` shadows the persistent flag: it is not the
        // output format switch there.
        if !node.local.iter().any(|flag| flag.name == "output") {
            output = parsed.string_value("output");
        }
        state = Some(parsed);
    }
    if !node.runnable {
        return Front::Exit(crate::help_cli::render(&key));
    }
    // cobra.OnInitialize(initConfig)
    if let Err(error) = crate::config_paths::load_root_config() {
        return Front::Exit(crate::write_stderr(
            &format!("failed to load config: {error}\n"),
            CoreExitCode::Config,
        ));
    }
    if let Some(message) = node.arity.check(&node.path, &positional) {
        return Front::Exit(report(&message, json));
    }
    // PersistentPreRun: --output is the canonical switch, --json its alias.
    if !output.is_empty() {
        match output.as_str() {
            "json" => json = true,
            "text" | "yaml" => json = false,
            _ => {
                return Front::Exit(crate::write_stderr(
                    &format!(
                        "invalid --output value {} (want text|json|yaml)\n",
                        go_quote(&output)
                    ),
                    CoreExitCode::Generic,
                ));
            }
        }
    }
    if let Some(state) = &state
        && let Some(message) = validate_flags(state)
    {
        return Front::Exit(report(&message, json));
    }
    match node.builtin {
        Builtin::Help => Front::Exit(run_help(tree, &positional)),
        Builtin::Completion(shell) => {
            let descriptions_off = state
                .as_ref()
                .is_some_and(|state| state.bool_value("no-descriptions"));
            let key = if descriptions_off {
                format!("{shell}-no-descriptions")
            } else {
                shell.to_owned()
            };
            match crate::help_cli::completion_script(&key) {
                Some(script) => Front::Exit(crate::write_stdout(script.to_owned())),
                None => Front::Exit(report(&format!("no completion script for {key}"), json)),
            }
        }
        Builtin::Complete => Front::Exit(complete::run(&positional, called_as)),
        Builtin::Native => Front::Continue(clap_arguments(tree, &found, state.as_ref(), original)),
    }
}

/// The argv handed to the Clap pipeline once Cobra accepted the command line.
///
/// Clap implements the commands' bodies, but cannot express pflag's grammar:
/// repetition (`--output a --output b`: the last one wins), explicit bool
/// values (`--json=false`), switched-off help (`--help=false`), a value that
/// looks like a flag (`--dir --help`), or a lone `-` / empty word before the
/// command. The line is therefore rebuilt from what pflag decided: the
/// persistent flags once with their final value, the canonical command path,
/// every bool flag that ended up true, then the remaining tokens with each
/// value-taking flag's final occurrence attached (`--flag=value`). A flag the
/// command defines itself (`index maintenance backup --output`) is treated
/// the same way but keeps its own name.
fn clap_arguments(
    tree: &Tree,
    found: &find::Found,
    state: Option<&State>,
    original: &[OsString],
) -> Vec<OsString> {
    let Some(state) = state else {
        return original.to_vec();
    };
    let node = tree.node(found.node);
    let flags = tree.flag_set(found.node, true);
    let mut dropped: Vec<usize> = Vec::new();
    let mut rewritten: Vec<(usize, String)> = Vec::new();
    let mut persistent: Vec<OsString> = Vec::new();
    let mut bools: Vec<OsString> = Vec::new();
    let mut names: Vec<&str> = Vec::new();
    for (_, _, name) in state.long_tokens() {
        if !names.contains(&name.as_str()) {
            names.push(name);
        }
    }
    for name in names {
        let occurrences = state.tokens_of(name);
        let Some(flag) = flags.lookup(name) else {
            continue;
        };
        let covered =
            |(start, length): &(usize, usize)| (*start..*start + *length).collect::<Vec<_>>();
        let shadowed = node.local.iter().any(|local| local.name == name);
        if name == "help" {
            dropped.extend(occurrences.iter().flat_map(covered));
        } else if matches!(name, "json" | "output" | "vault") && !shadowed {
            dropped.extend(occurrences.iter().flat_map(covered));
            match name {
                "json" if state.bool_value("json") => persistent.push(OsString::from("--json")),
                "output" | "vault" if state.changed(name) => persistent.push(OsString::from(
                    format!("--{name}={}", state.string_value(name)),
                )),
                _ => {}
            }
        } else if flag.kind == pflag::FlagKind::Bool {
            dropped.extend(occurrences.iter().flat_map(covered));
            if name == "local-worker" && state.changed(name) {
                // `serve` lets an explicit `--local-worker=false` beat the Home
                // Assistant options, so the flag keeps its "changed" state.
                bools.push(OsString::from(format!(
                    "--{name}={}",
                    state.bool_value(name)
                )));
            } else if state.bool_value(name) {
                bools.push(OsString::from(format!("--{name}")));
            }
        } else {
            let keep_from = if flag.takes_multiple() {
                0
            } else {
                occurrences.len() - 1
            };
            for (position, occurrence) in occurrences.iter().enumerate() {
                if position < keep_from {
                    dropped.extend(covered(occurrence));
                } else if occurrence.1 == 2 {
                    // `--name value` -> `--name=value`.
                    rewritten.push((
                        occurrence.0,
                        format!("--{name}={}", found.args[occurrence.0 + 1]),
                    ));
                    dropped.push(occurrence.0 + 1);
                }
            }
        }
    }
    let mut arguments: Vec<OsString> = vec![original[0].clone()];
    arguments.extend(persistent);
    arguments.extend(node.path.split(' ').skip(1).map(OsString::from));
    arguments.extend(bools);
    for (index, token) in found.args.iter().enumerate() {
        if let Some((_, replacement)) = rewritten.iter().find(|(start, _)| *start == index) {
            arguments.push(OsString::from(replacement));
        } else if !dropped.contains(&index) {
            arguments.push(OsString::from(token));
        }
    }
    arguments
}

/// `ValidateRequiredFlags` then `ValidateFlagGroups`.
fn validate_flags(state: &State) -> Option<String> {
    let set = state.set();
    let missing: Vec<String> = set
        .sorted()
        .into_iter()
        .filter(|flag| flag.one_required && !state.changed(&flag.name))
        .map(|flag| flag.name.clone())
        .collect();
    if !missing.is_empty() {
        return Some(format!(
            "required flag(s) \"{}\" not set",
            missing.join("\", \"")
        ));
    }
    let mut groups: Vec<String> = Vec::new();
    for flag in set.sorted() {
        for group in &flag.exclusive_groups {
            if !groups.contains(group) {
                groups.push(group.clone());
            }
        }
    }
    groups.sort();
    for group in groups {
        let names: Vec<&str> = group.split(' ').collect();
        // Only consider a group when every flag in it is defined.
        if names.iter().any(|name| set.lookup(name).is_none()) {
            continue;
        }
        let mut changed: Vec<&str> = names
            .iter()
            .copied()
            .filter(|name| state.changed(name))
            .collect();
        if changed.len() > 1 {
            changed.sort_unstable();
            return Some(format!(
                "if any flags in the group [{group}] are set none of the others can be; [{}] were all set",
                changed.join(" ")
            ));
        }
    }
    None
}

/// The generated `help [command]`.
fn run_help(tree: &Tree, topics: &[String]) -> ExitCode {
    let found = find(tree, topics, false);
    if found.error.is_some() {
        let shown = topics
            .iter()
            .map(|topic| go_sharp_quote(topic))
            .collect::<Vec<_>>()
            .join(" ");
        return crate::write_stderr(
            &format!(
                "Unknown help topic [{shown}]\n{}",
                crate::help_cli::root_usage()
            ),
            CoreExitCode::Ok,
        );
    }
    crate::help_cli::render(tree.help_key(found.node))
}
