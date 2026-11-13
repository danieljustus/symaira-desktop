//! `cobra.Command.Find` and its helpers, ported from Cobra v1.10.2.
use super::pflag::FlagSet;
use super::quote::go_quote;
use super::tree::{Arity, Builtin, Tree};

pub struct Found {
    pub node: usize,
    /// The arguments left for the command (its own name tokens removed).
    pub args: Vec<String>,
    pub error: Option<String>,
    /// `Command.CalledAs()`: the name or alias the command was reached by.
    pub called_as: String,
}

/// `stripFlags`: the non-flag words, in order. Cobra drops a flag's value
/// without knowing the flag when `--help`/`-h` is not registered yet.
pub fn strip_flags(args: &[String], flags: &FlagSet) -> Vec<String> {
    let mut commands = Vec::new();
    let mut rest = args;
    while let Some((current, tail)) = rest.split_first() {
        rest = tail;
        let s = current.as_str();
        if s == "--" {
            break;
        }
        let long_with_value =
            s.starts_with("--") && !s.contains('=') && !flags.has_no_opt_def_val(&s[2..]);
        let short_with_value = s.starts_with('-')
            && !s.contains('=')
            && s.len() == 2
            && !flags.short_has_no_opt_def_val(&s[1..]);
        if long_with_value || short_with_value {
            // `--flag arg`: delete the argument, or stop when at most one is left.
            if rest.len() <= 1 {
                break;
            }
            rest = &rest[1..];
            continue;
        }
        if !s.is_empty() && !s.starts_with('-') {
            commands.push(s.to_owned());
        }
    }
    commands
}

/// `argsMinusFirstX`.
fn args_minus_first_x(args: &[String], x: &str, flags: &FlagSet) -> Vec<String> {
    let mut position = 0;
    while position < args.len() {
        let s = args[position].as_str();
        if s == "--" {
            break;
        }
        let long_with_value =
            s.starts_with("--") && !s.contains('=') && !flags.has_no_opt_def_val(&s[2..]);
        let short_with_value = s.starts_with('-')
            && !s.contains('=')
            && s.len() == 2
            && !flags.short_has_no_opt_def_val(&s[1..]);
        if long_with_value || short_with_value {
            position += 2;
            continue;
        }
        if !s.starts_with('-') && s == x {
            let mut result = args[..position].to_vec();
            result.extend_from_slice(&args[position + 1..]);
            return result;
        }
        position += 1;
    }
    args.to_vec()
}

/// Cobra's Levenshtein distance (`ld(s, t, ignoreCase)`) over bytes.
fn levenshtein(s: &str, t: &str) -> usize {
    let s = s.to_lowercase().into_bytes();
    let t = t.to_lowercase().into_bytes();
    let mut d = vec![vec![0usize; t.len() + 1]; s.len() + 1];
    for (i, row) in d.iter_mut().enumerate() {
        row[0] = i;
    }
    for (j, cell) in d[0].iter_mut().enumerate() {
        *cell = j;
    }
    for j in 1..=t.len() {
        for i in 1..=s.len() {
            d[i][j] = if s[i - 1] == t[j - 1] {
                d[i - 1][j - 1]
            } else {
                1 + d[i - 1][j].min(d[i][j - 1]).min(d[i - 1][j - 1])
            };
        }
    }
    d[s.len()][t.len()]
}

fn suggestions(tree: &Tree, command: usize, typed: &str) -> String {
    let mut names = Vec::new();
    for child in tree.node(command).children.iter().copied() {
        if !tree.is_available(child) {
            continue;
        }
        let name = &tree.node(child).name;
        if levenshtein(typed, name) <= 2 || name.to_lowercase().starts_with(&typed.to_lowercase()) {
            names.push(name.clone());
        }
    }
    if names.is_empty() {
        return String::new();
    }
    let mut text = String::from("\n\nDid you mean this?\n");
    for name in names {
        text.push_str(&format!("\t{name}\n"));
    }
    text
}

/// `Command.Find` on the root.
pub fn find(tree: &Tree, args: &[String], allow_complete: bool) -> Found {
    let mut command = tree.root;
    let mut inner = args.to_vec();
    let mut called_as = tree.node(command).name.clone();
    loop {
        let flags = tree.flag_set(command, false);
        let stripped = strip_flags(&inner, &flags);
        let Some(next) = stripped.first() else { break };
        let Some(child) = tree.find_next(command, next) else {
            break;
        };
        // `__complete` only exists while it is the command being run.
        if !allow_complete && tree.node(child).builtin == Builtin::Complete {
            break;
        }
        inner = args_minus_first_x(&inner, next, &flags);
        called_as = next.clone();
        command = child;
    }
    let mut error = None;
    if tree.node(command).arity == Arity::Legacy {
        let flags = tree.flag_set(command, false);
        let stripped = strip_flags(&inner, &flags);
        // legacyArgs: only the root, which has subcommands, rejects words.
        if tree.has_subcommands(command)
            && tree.node(command).parent.is_none()
            && !stripped.is_empty()
        {
            error = Some(format!(
                "unknown command {} for {}{}",
                go_quote(&stripped[0]),
                go_quote(&path_for_error(tree, command)),
                suggestions(tree, command, &stripped[0])
            ));
        }
    }
    Found {
        node: command,
        args: inner,
        error,
        called_as,
    }
}

/// `CommandPath()`.
pub fn path_for_error(tree: &Tree, command: usize) -> String {
    tree.node(command).path.clone()
}

#[cfg(test)]
mod tests {
    use super::levenshtein;

    #[test]
    fn distance_is_case_insensitive() {
        assert_eq!(levenshtein("LSS", "ls"), 1);
        assert_eq!(levenshtein("kitten", "sitting"), 3);
    }
}
