//! Cobra's hidden `__complete` / `__completeNoDesc` request command
//! (`getCompletions`, v1.10.2). This tree registers no `ValidArgs`, no flag
//! completion functions and no completion annotations; only the generated
//! `help [command]` completes arguments.
use std::process::ExitCode;

use symaira_core_exit::ExitCode as CoreExitCode;

use super::{
    find::find,
    pflag::{self, Flag, FlagSet},
    tree::{Builtin, Tree},
};

const NO_SPACE: u32 = 2;
const NO_FILE_COMP: u32 = 4;
const DEFAULT: u32 = 0;

fn directive_text(directive: u32) -> String {
    let mut parts = Vec::new();
    for (bit, name) in [
        (1, "ShellCompDirectiveError"),
        (2, "ShellCompDirectiveNoSpace"),
        (4, "ShellCompDirectiveNoFileComp"),
        (8, "ShellCompDirectiveFilterFileExt"),
        (16, "ShellCompDirectiveFilterDirs"),
        (32, "ShellCompDirectiveKeepOrder"),
    ] {
        if directive & bit != 0 {
            parts.push(name);
        }
    }
    if parts.is_empty() {
        parts.push("ShellCompDirectiveDefault");
    }
    parts.join(", ")
}

fn is_flag_arg(arg: &str) -> bool {
    (arg.len() >= 3 && arg.starts_with("--"))
        || (arg.len() >= 2 && arg.starts_with('-') && !arg.starts_with("--"))
}

/// Go's `%v` for a `[]string`.
fn go_list(values: &[String]) -> String {
    format!("[{}]", values.join(" "))
}

struct Result {
    completions: Vec<String>,
    directive: u32,
    error: Option<String>,
}

fn with_desc(name: &str, description: &str) -> String {
    format!("{name}\t{description}")
}

fn find_flag<'a>(set: &'a FlagSet, name: &str) -> Option<&'a Flag> {
    if name.len() == 1
        && let Some(short) = set.shorthand(name.as_bytes()[0])
    {
        return set.lookup(&short.name);
    }
    set.lookup(name)
}

fn flag_name_completions(flag: &Flag, to_complete: &str) -> Vec<String> {
    if flag.hidden {
        return Vec::new();
    }
    let mut completions = Vec::new();
    let long = format!("--{}", flag.name);
    if long.starts_with(to_complete) {
        completions.push(with_desc(&long, &flag.usage));
    }
    if let Some(shorthand) = flag.shorthand {
        let short = format!("-{}", char::from(shorthand));
        if short.starts_with(to_complete) {
            completions.push(with_desc(&short, &flag.usage));
        }
    }
    completions
}

fn complete_required_flags(
    tree: &Tree,
    command: usize,
    set: &FlagSet,
    changed: &dyn Fn(&str) -> bool,
    to_complete: &str,
) -> Vec<String> {
    let inherited = tree.inherited_flags(command);
    let mut completions = Vec::new();
    let visit = |flag: &Flag, completions: &mut Vec<String>| {
        if flag.one_required && !changed(&flag.name) {
            completions.extend(flag_name_completions(flag, to_complete));
        }
    };
    for flag in &inherited {
        visit(flag, &mut completions);
    }
    for flag in non_inherited(set, &inherited) {
        visit(flag, &mut completions);
    }
    completions
}

fn non_inherited<'a>(set: &'a FlagSet, inherited: &[Flag]) -> Vec<&'a Flag> {
    set.sorted()
        .into_iter()
        .filter(|flag| !inherited.iter().any(|other| other.name == flag.name))
        .collect()
}

/// Cobra's `checkIfFlagCompletion`: the flag whose value is being completed,
/// the arguments to parse, the word to complete and a `flagCompError`.
fn check_if_flag_completion(
    set: &FlagSet,
    command_name: &str,
    args: &[String],
    last_arg: &str,
) -> (Option<Flag>, Vec<String>, String, Option<String>) {
    let mut flag_name = String::new();
    let mut trimmed = args.to_vec();
    let mut flag_with_equal = false;
    let mut last = last_arg.to_owned();
    if last.starts_with('-') {
        let Some(index) = last.find('=') else {
            // Normal flag completion.
            return (None, args.to_vec(), last, None);
        };
        flag_name = match last[..index].strip_prefix("--") {
            Some(long) => long.to_owned(),
            None => last.get(index - 1..index).unwrap_or_default().to_owned(),
        };
        last = last[index + 1..].to_owned();
        flag_with_equal = true;
    }
    if flag_name.is_empty()
        && let Some(previous) = args.last()
        && is_flag_arg(previous)
        && !previous.contains('=')
    {
        flag_name = match previous.strip_prefix("--") {
            Some(long) => long.to_owned(),
            None => previous
                .get(previous.len() - 1..)
                .unwrap_or_default()
                .to_owned(),
        };
        trimmed = args[..args.len() - 1].to_vec();
    }
    if flag_name.is_empty() {
        // Not doing flag completion.
        return (None, trimmed, last, None);
    }
    let Some(flag) = find_flag(set, &flag_name) else {
        return (
            None,
            args.to_vec(),
            last_arg.to_owned(),
            Some(format!(
                "Subcommand '{command_name}' does not support flag '{flag_name}'"
            )),
        );
    };
    if !flag_with_equal && flag.no_opt_default.is_some() {
        // A bool flag has no value to complete: noun completion instead.
        return (None, args.to_vec(), last, None);
    }
    (Some(flag.clone()), trimmed, last, None)
}

fn compute(tree: &Tree, request: &[String]) -> (usize, Result) {
    let complete = tree.by_path("symdesk __complete").expect("__complete node");
    let to_complete = request.last().cloned().unwrap_or_default();
    let trimmed = &request[..request.len().saturating_sub(1)];
    let found = find(tree, trimmed, true);
    if found.error.is_some() {
        return (
            complete,
            Result {
                completions: Vec::new(),
                directive: DEFAULT,
                error: Some(format!(
                    "unable to find a command for arguments: {}",
                    go_list(trimmed)
                )),
            },
        );
    }
    let command = found.node;
    let node = tree.node(command);
    let set = tree.flag_set(command, !node.disable_flag_parsing);
    let (flag, mut final_args, to_complete, flag_error) = if node.disable_flag_parsing {
        (None, found.args.clone(), to_complete, None)
    } else {
        check_if_flag_completion(&set, &node.name, &found.args, &to_complete)
    };

    let mut flag_completion = true;
    let mut parse_state = None;
    if !node.disable_flag_parsing {
        let mut with_dash = final_args.clone();
        with_dash.push("--".to_owned());
        let (state, _) = pflag::parse(&set, &with_dash);
        let new_arg_count = state.args.len();
        let (state, result) = pflag::parse(&set, &final_args);
        if let Err(message) = result {
            return (
                command,
                Result {
                    completions: Vec::new(),
                    directive: DEFAULT,
                    error: Some(format!(
                        "Error while parsing flags from args {}: {message}",
                        go_list(&final_args)
                    )),
                },
            );
        }
        if new_arg_count > state.args.len() {
            flag_completion = false;
        }
        parse_state = Some(state);
    }
    if let Some(message) = flag_error
        && flag_completion
    {
        return (
            command,
            Result {
                completions: Vec::new(),
                directive: DEFAULT,
                error: Some(message),
            },
        );
    }
    let changed = |name: &str| {
        parse_state
            .as_ref()
            .is_some_and(|state| state.changed(name))
    };
    // helpOrVersionFlagPresent
    if parse_state.is_some() && (changed("help") || (command == tree.root && changed("version"))) {
        return (
            command,
            Result {
                completions: Vec::new(),
                directive: NO_FILE_COMP,
                error: None,
            },
        );
    }
    if let Some(state) = &parse_state {
        final_args = state.args.clone();
    }

    // enforceFlagGroupsForCompletion: a mutually exclusive flag hides its peers.
    let mut set = set;
    let mut hidden_by_group: Vec<String> = Vec::new();
    if !node.disable_flag_parsing {
        for candidate in set.flags() {
            for group in &candidate.exclusive_groups {
                let members: Vec<&str> = group.split(' ').collect();
                if members.iter().any(|member| set.lookup(member).is_none()) {
                    continue;
                }
                if members.iter().any(|member| changed(member)) {
                    for member in &members {
                        if !changed(member) {
                            hidden_by_group.push((*member).to_owned());
                        }
                    }
                }
            }
        }
        let mut hidden_set = FlagSet::default();
        for existing in set.flags() {
            let mut copy = existing.clone();
            if hidden_by_group.contains(&copy.name) {
                copy.hidden = true;
            }
            hidden_set.add(copy);
        }
        set = hidden_set;
    }

    let mut completions: Vec<String> = Vec::new();
    let directive;
    if flag.is_none()
        && to_complete.starts_with('-')
        && !to_complete.contains('=')
        && flag_completion
    {
        completions = complete_required_flags(tree, command, &set, &changed, &to_complete);
        if completions.is_empty() {
            let inherited = tree.inherited_flags(command);
            let mut do_complete = |candidate: &Flag| {
                if !changed(&candidate.name) || candidate.takes_multiple() {
                    let live = set.lookup(&candidate.name).unwrap_or(candidate);
                    completions.extend(flag_name_completions(live, &to_complete));
                }
            };
            for candidate in &inherited {
                do_complete(candidate);
            }
            for candidate in non_inherited(&set, &inherited) {
                do_complete(candidate);
            }
        }
        directive = if completions.len() == 1 && completions[0].ends_with('=') {
            NO_SPACE
        } else {
            NO_FILE_COMP
        };
        if !node.disable_flag_parsing {
            return (
                command,
                Result {
                    completions,
                    directive,
                    error: None,
                },
            );
        }
    } else {
        let mut chosen = DEFAULT;
        if flag.is_none() {
            // A changed local, non-persistent flag stops subcommand completion.
            let inherited = tree.inherited_flags(command);
            let found_local = non_inherited(&set, &inherited).iter().any(|candidate| {
                let persistent = tree
                    .node(command)
                    .persistent
                    .iter()
                    .any(|own| own.name == candidate.name);
                !persistent && changed(&candidate.name)
            });
            if final_args.is_empty() && !found_local {
                for child in tree.sorted_children(command) {
                    let is_help = command == tree.root && tree.node(child).name == "help";
                    if tree.is_available(child) || is_help {
                        let name = &tree.node(child).name;
                        if name.starts_with(&to_complete) {
                            completions.push(with_desc(name, &tree.node(child).short));
                        }
                        chosen = NO_FILE_COMP;
                    }
                }
            }
            completions.extend(complete_required_flags(
                tree,
                command,
                &set,
                &changed,
                &to_complete,
            ));
        }
        directive = chosen;
    }

    // The only completion function: `help [command]`.
    let mut directive = directive;
    if (flag.is_none() || !flag_completion) && tree.node(command).builtin == Builtin::Help {
        let lookup = find(tree, &final_args, true);
        if lookup.error.is_none() {
            for child in tree.sorted_children(lookup.node) {
                let is_help = lookup.node == tree.root && tree.node(child).name == "help";
                if (tree.is_available(child) || is_help)
                    && tree.node(child).name.starts_with(&to_complete)
                {
                    completions.push(with_desc(&tree.node(child).name, &tree.node(child).short));
                }
            }
        }
        directive = NO_FILE_COMP;
    }
    // `completion` and its shells register `NoFileCompletions`.
    if tree.node(command).path.starts_with("symdesk completion")
        && (flag.is_none() || !flag_completion)
    {
        directive = NO_FILE_COMP;
    }
    (
        command,
        Result {
            completions,
            directive,
            error: None,
        },
    )
}

pub fn run(request: &[String], called_as: &str) -> ExitCode {
    let tree = Tree::get();
    let (_, result) = compute(tree, request);
    let mut stdout = String::new();
    let mut stderr = String::new();
    if let Some(message) = &result.error {
        stderr.push_str(&format!("[Debug] [Error] {message}\n"));
    }
    let mut descriptions = called_as != "__completeNoDesc";
    if descriptions {
        for name in [
            "SYMDESK_COMPLETION_DESCRIPTIONS",
            "COBRA_COMPLETION_DESCRIPTIONS",
        ] {
            let value = std::env::var(name).unwrap_or_default();
            if value.is_empty() {
                continue;
            }
            if let Ok(parsed) = parse_bool(&value) {
                descriptions = parsed;
            }
            break;
        }
    }
    for completion in &result.completions {
        let mut text = completion.clone();
        if !descriptions {
            text = text.split('\t').next().unwrap_or("").to_owned();
        }
        text = text.split('\n').next().unwrap_or("").to_owned();
        stdout.push_str(text.trim());
        stdout.push('\n');
    }
    stdout.push_str(&format!(":{}\n", result.directive));
    stderr.push_str(&format!(
        "Completion ended with directive: {}\n",
        directive_text(result.directive)
    ));
    use std::io::Write;
    if std::io::stdout().write_all(stdout.as_bytes()).is_err()
        || std::io::stderr().write_all(stderr.as_bytes()).is_err()
    {
        return ExitCode::from(CoreExitCode::Generic.as_u8());
    }
    ExitCode::from(CoreExitCode::Ok.as_u8())
}

fn parse_bool(value: &str) -> core::result::Result<bool, ()> {
    match value {
        "1" | "t" | "T" | "TRUE" | "true" | "True" => Ok(true),
        "0" | "f" | "F" | "FALSE" | "false" | "False" => Ok(false),
        _ => Err(()),
    }
}
