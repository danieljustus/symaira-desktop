//! Commands that did not exist in the Go oracle (docs/rust-port/cli-additions.md).
//!
//! The recorded Cobra documents (`testdata/port/cli/cobra-help.json`) are
//! history and never change. New commands are declared in
//! `assets/cli-additions.json`; this module inserts one list line per
//! declaration into the recorded document of the parent node, with Cobra's own
//! rules (alphabetical within the group, name column `max(11, longest)` wide).
//! It is std + serde only because the integration tests include this file.
use std::collections::BTreeMap;

use serde::Deserialize;

/// Cobra's `minNamePadding`.
const MIN_NAME_PADDING: usize = 11;

#[derive(Debug, Clone, Deserialize, PartialEq, Eq)]
#[serde(deny_unknown_fields)]
pub struct Addition {
    /// Help key of the frozen parent node: `""` is the root, `meeting` is `symdesk meeting`.
    pub parent: String,
    pub name: String,
    /// Heading of the command list in the parent's document, without the colon.
    pub group: String,
    pub short: String,
    pub help: Help,
}

/// How `symdesk help <path>` answers for the added command.
#[derive(Debug, Clone, Deserialize, PartialEq, Eq)]
#[serde(tag = "source", rename_all = "lowercase", deny_unknown_fields)]
pub enum Help {
    /// The linked-in journal's own usage (`symroom [words] --help`); root `room` only.
    Symroom,
    /// The document itself.
    Text { text: String },
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct File {
    schema_version: u32,
    additions: Vec<Addition>,
}

impl Addition {
    fn check(&self) -> Result<(), String> {
        let name = &self.name;
        if name.is_empty()
            || name.starts_with('-')
            || name.starts_with("__")
            || name.chars().any(|c| c.is_whitespace() || c.is_control())
        {
            return Err(format!("invalid command name {name:?}"));
        }
        if self.short.is_empty() || self.short.chars().any(char::is_control) {
            return Err(format!("{name}: short must be one non-empty line"));
        }
        if !self.group.ends_with("Commands") {
            return Err(format!(
                "{name}: group {:?} is not a command list",
                self.group
            ));
        }
        match &self.help {
            Help::Symroom if self.parent.is_empty() && name == "room" => Ok(()),
            Help::Symroom => Err(format!(
                "{name}: the symroom help source is for the root `room` only"
            )),
            Help::Text { text } if text.ends_with('\n') => Ok(()),
            Help::Text { .. } => Err(format!("{name}: help text must end with a newline")),
        }
    }
}

pub fn parse(json: &str) -> Result<Vec<Addition>, String> {
    let file: File = serde_json::from_str(json).map_err(|error| error.to_string())?;
    if file.schema_version != 1 {
        return Err(format!(
            "unsupported schema_version {}",
            file.schema_version
        ));
    }
    for (index, addition) in file.additions.iter().enumerate() {
        addition.check()?;
        if file.additions[..index]
            .iter()
            .any(|other| other.parent == addition.parent && other.name == addition.name)
        {
            return Err(format!("{}: declared twice", addition.name));
        }
    }
    Ok(file.additions)
}

/// The additions this product declares, validated on first use.
pub fn declared() -> &'static [Addition] {
    static DECLARED: std::sync::OnceLock<Vec<Addition>> = std::sync::OnceLock::new();
    DECLARED.get_or_init(|| {
        parse(include_str!("../assets/cli-additions.json")).expect("assets/cli-additions.json")
    })
}

/// A list entry of a Cobra command list: `  <name padded> <short>`.
struct Entry<'a> {
    name: &'a str,
    short: &'a str,
}

fn is_list_heading(line: &str) -> bool {
    !line.starts_with(' ') && line.ends_with("Commands:")
}

fn parse_entry(line: &str) -> Result<(Entry<'_>, usize), String> {
    let rest = line
        .strip_prefix("  ")
        .ok_or_else(|| format!("not a command list entry: {line:?}"))?;
    let name_end = rest
        .find(' ')
        .ok_or_else(|| format!("entry without description: {line:?}"))?;
    let (name, tail) = rest.split_at(name_end);
    let short = tail.trim_start_matches(' ');
    if short.is_empty() {
        return Err(format!("entry without description: {line:?}"));
    }
    // Cobra: rpad(name, padding) + " " + short.
    let padding = name.len() + (tail.len() - short.len()) - 1;
    Ok((Entry { name, short }, padding))
}

/// `doc` of the node `key` with every addition declared for that node inserted.
pub fn augment(key: &str, doc: &str, additions: &[Addition]) -> Result<String, String> {
    let mut text = doc.to_owned();
    for addition in additions.iter().filter(|a| a.parent == key) {
        text = insert(&text, addition)?;
    }
    Ok(text)
}

fn insert(doc: &str, addition: &Addition) -> Result<String, String> {
    let mut lines: Vec<String> = doc.split('\n').map(str::to_owned).collect();
    let heading = format!("{}:", addition.group);
    // (heading index, entry range) of every command list.
    let mut lists: Vec<(usize, std::ops::Range<usize>)> = Vec::new();
    let mut index = 0;
    while index < lines.len() {
        if is_list_heading(&lines[index]) {
            let start = index + 1;
            let mut end = start;
            while end < lines.len() && !lines[end].is_empty() {
                end += 1;
            }
            lists.push((index, start..end));
            index = end;
        } else {
            index += 1;
        }
    }
    let mut padding: Option<usize> = None;
    for (_, range) in &lists {
        for line in &lines[range.clone()] {
            let (entry, own) = parse_entry(line)?;
            if *padding.get_or_insert(own) != own {
                return Err(format!("inconsistent name padding at {line:?}"));
            }
            if entry.name == addition.name {
                return Err(format!(
                    "{}: already a command of this document",
                    addition.name
                ));
            }
        }
    }
    let padding = padding.ok_or_else(|| "document has no command list".to_owned())?;
    let (_, target) = lists
        .iter()
        .find(|(at, _)| lines[*at] == heading)
        .ok_or_else(|| format!("{}: no `{heading}` list in this document", addition.name))?;
    let mut position = target.end;
    for at in target.clone() {
        if parse_entry(&lines[at])?.0.name > addition.name.as_str() {
            position = at;
            break;
        }
    }
    let widened = padding.max(addition.name.len()).max(MIN_NAME_PADDING);
    if widened != padding {
        for (_, range) in &lists {
            for line in &mut lines[range.clone()] {
                let (entry, _) = parse_entry(line)?;
                *line = format!("  {:<widened$} {}", entry.name, entry.short);
            }
        }
    }
    lines.insert(
        position,
        format!("  {:<widened$} {}", addition.name, addition.short),
    );
    Ok(lines.join("\n"))
}

/// Apply the additions to the recorded documents; every `parent` must be a
/// recorded help key.
pub fn apply(
    help: &mut BTreeMap<String, String>,
    root_usage: &mut String,
    additions: &[Addition],
) -> Result<(), String> {
    for addition in additions {
        if !help.contains_key(&addition.parent) {
            return Err(format!(
                "{}: parent {:?} is not a recorded command",
                addition.name, addition.parent
            ));
        }
    }
    for (key, doc) in help.iter_mut() {
        *doc = augment(key, doc, additions)?;
    }
    *root_usage = augment("", root_usage, additions)?;
    Ok(())
}

/// The addition whose command path (`parent` words + `name`) is a prefix of
/// `topics`, with the number of words it consumed.
pub fn find_help<'a>(
    additions: &'a [Addition],
    topics: &[String],
) -> Option<(&'a Addition, usize)> {
    additions.iter().find_map(|addition| {
        let path: Vec<&str> = addition
            .parent
            .split(' ')
            .filter(|word| !word.is_empty())
            .chain(std::iter::once(addition.name.as_str()))
            .collect();
        let matches =
            topics.len() >= path.len() && path.iter().zip(topics).all(|(want, got)| want == got);
        matches.then_some((addition, path.len()))
    })
}
