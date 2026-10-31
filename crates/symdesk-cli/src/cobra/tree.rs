//! The Cobra command tree, rebuilt from the Go-generated inventory
//! (`testdata/port/cli/symdesk-command-tree.json`).
//!
//! Every fact here (names, aliases, visibility, flags, argument validators) is
//! recorded from the real Cobra tree; nothing is inferred from the Rust
//! command definitions.
use std::{collections::HashMap, sync::OnceLock};

use serde::Deserialize;

use super::pflag::{Flag, FlagKind, FlagSet};

#[derive(Deserialize)]
struct TreeDocument {
    commands: Vec<Spec>,
}

#[derive(Deserialize)]
struct Spec {
    path: String,
    name: String,
    short: String,
    #[serde(default)]
    aliases: Vec<String>,
    hidden: bool,
    runnable: bool,
    disable_flag_parsing: bool,
    #[serde(default)]
    argument_count_probes: Vec<Probe>,
    #[serde(default)]
    local_flags: Vec<FlagSpec>,
    #[serde(default)]
    persistent_flags: Vec<FlagSpec>,
}

#[derive(Deserialize)]
struct Probe {
    accepted: bool,
}

#[derive(Deserialize)]
struct FlagSpec {
    name: String,
    #[serde(default)]
    shorthand: Option<String>,
    usage: String,
    #[serde(rename = "type")]
    kind: String,
    default: String,
    #[serde(default)]
    no_opt_default: Option<String>,
    hidden: bool,
    #[serde(default)]
    annotations: HashMap<String, Vec<String>>,
}

const SET_BY_COBRA: &str = "cobra_annotation_flag_set_by_cobra";
const ONE_REQUIRED: &str = "cobra_annotation_bash_completion_one_required_flag";
const MUTUALLY_EXCLUSIVE: &str = "cobra_annotation_mutually_exclusive";

/// Cobra's positional-argument validators, rebuilt from the recorded probes
/// (counts 0..=9). `Legacy` is `Args == nil`.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Arity {
    Legacy,
    Arbitrary,
    None,
    Exact(usize),
    AtMost(usize),
    AtLeast(usize),
    Range(usize, usize),
}

impl Arity {
    /// Cobra's validator error for `args`, or `None` when accepted.
    pub fn check(self, command_path: &str, args: &[String]) -> Option<String> {
        let received = args.len();
        match self {
            Self::Legacy | Self::Arbitrary => None,
            Self::None => (received > 0).then(|| {
                format!(
                    "unknown command {} for {}",
                    super::quote::go_quote(&args[0]),
                    super::quote::go_quote(command_path)
                )
            }),
            Self::Exact(n) => {
                (received != n).then(|| format!("accepts {n} arg(s), received {received}"))
            }
            Self::AtMost(n) => {
                (received > n).then(|| format!("accepts at most {n} arg(s), received {received}"))
            }
            Self::AtLeast(n) => (received < n)
                .then(|| format!("requires at least {n} arg(s), only received {received}")),
            Self::Range(min, max) => (received < min || received > max)
                .then(|| format!("accepts between {min} and {max} arg(s), received {received}")),
        }
    }

    fn from_probes(path: &str, probes: &[Probe]) -> Self {
        if probes.is_empty() {
            return Self::Legacy;
        }
        let accepted: Vec<bool> = probes.iter().map(|probe| probe.accepted).collect();
        let first = accepted.iter().position(|accepted| *accepted);
        let last = accepted.iter().rposition(|accepted| *accepted);
        let (Some(first), Some(last)) = (first, last) else {
            unreachable!("{path}: every probe rejected")
        };
        let tail = accepted.len() - 1;
        if first == 0 && last == tail {
            return Self::Arbitrary;
        }
        if last == tail {
            return Self::AtLeast(first);
        }
        match (first, last) {
            (0, 0) => Self::None,
            (0, last) => Self::AtMost(last),
            (first, last) if first == last => Self::Exact(first),
            (first, last) => Self::Range(first, last),
        }
    }
}

/// How Rust executes a runnable node.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Builtin {
    /// Cobra's generated `help [command]`.
    Help,
    /// Cobra's generated `completion <shell>` (the shell name).
    Completion(&'static str),
    /// Cobra's hidden `__complete` / `__completeNoDesc`.
    Complete,
    /// A command whose behaviour lives in the Clap pipeline.
    Native,
}

pub struct Node {
    pub name: String,
    pub path: String,
    pub short: String,
    pub aliases: Vec<String>,
    pub hidden: bool,
    pub runnable: bool,
    pub disable_flag_parsing: bool,
    pub arity: Arity,
    pub parent: Option<usize>,
    /// Registration order for the root (Cobra's `c.commands`), sorted by name
    /// elsewhere. Suggestions follow this order, completion the sorted one.
    pub children: Vec<usize>,
    pub local: Vec<Flag>,
    pub persistent: Vec<Flag>,
    pub builtin: Builtin,
}

pub struct Tree {
    pub nodes: Vec<Node>,
    pub root: usize,
    by_path: HashMap<String, usize>,
}

/// `Cobra`'s `c.commands` of the root before `ExecuteC` appends `help`,
/// `__complete` and `completion`: the Go registration order, recorded by the
/// process generator (`root_registration_order`) and checked against it.
pub const ROOT_REGISTRATION_ORDER: &str =
    include_str!("../../../../testdata/port/cli/cobra-root-order.txt");

impl Tree {
    pub fn get() -> &'static Self {
        static TREE: OnceLock<Tree> = OnceLock::new();
        TREE.get_or_init(Self::load)
    }

    fn load() -> Self {
        let document: TreeDocument = serde_json::from_str(include_str!(
            "../../../../testdata/port/cli/symdesk-command-tree.json"
        ))
        .expect("Go-generated Cobra tree");
        let mut nodes: Vec<Node> = Vec::new();
        let mut by_path: HashMap<String, usize> = HashMap::new();
        // Parents sort before children by path, so a single pass links them.
        let mut specs = document.commands;
        specs.sort_by(|left, right| left.path.cmp(&right.path));
        for spec in specs {
            let index = nodes.len();
            let words: Vec<&str> = spec.path.split(' ').collect();
            let parent = (words.len() > 1).then(|| {
                *by_path
                    .get(&words[..words.len() - 1].join(" "))
                    .expect("parent precedes child")
            });
            let builtin = builtin_for(&words);
            let arity = Arity::from_probes(&spec.path, &spec.argument_count_probes);
            let mut local = Vec::new();
            for flag in spec.local_flags {
                // Cobra adds `--help` when a command executes, never before.
                if flag.name == "help" && flag.annotations.contains_key(SET_BY_COBRA) {
                    continue;
                }
                local.push(flag.into_flag());
            }
            let persistent = spec
                .persistent_flags
                .into_iter()
                .map(FlagSpec::into_flag)
                .collect();
            by_path.insert(spec.path.clone(), index);
            nodes.push(Node {
                name: spec.name,
                path: spec.path,
                short: spec.short,
                aliases: spec.aliases,
                hidden: spec.hidden,
                runnable: spec.runnable,
                disable_flag_parsing: spec.disable_flag_parsing,
                arity,
                parent,
                children: Vec::new(),
                local,
                persistent,
                builtin,
            });
            if let Some(parent) = parent {
                nodes[parent].children.push(index);
            }
        }
        let root = by_path["symdesk"];
        // Hidden `__complete` is only registered when it is the command being
        // run; an always-present node resolves identically in `Find`.
        let complete = nodes.len();
        nodes.push(Node {
            name: "__complete".to_owned(),
            path: "symdesk __complete".to_owned(),
            short: "Request shell completion choices for the specified command-line".to_owned(),
            aliases: vec!["__completeNoDesc".to_owned()],
            hidden: true,
            runnable: true,
            disable_flag_parsing: true,
            arity: Arity::AtLeast(1),
            parent: Some(root),
            children: Vec::new(),
            local: Vec::new(),
            persistent: Vec::new(),
            builtin: Builtin::Complete,
        });
        by_path.insert("symdesk __complete".to_owned(), complete);
        nodes[root].children.push(complete);
        // Registration order for the root: recorded Go order, then the
        // commands Cobra appends (`help`, `__complete`, `completion`).
        let mut ordered: Vec<usize> = Vec::new();
        for name in ROOT_REGISTRATION_ORDER.lines() {
            let path = format!("symdesk {name}");
            ordered.push(by_path[&path]);
        }
        for name in ["help", "__complete", "completion"] {
            ordered.push(by_path[&format!("symdesk {name}")]);
        }
        debug_assert_eq!(ordered.len(), nodes[root].children.len());
        nodes[root].children = ordered;
        // Every other parent: Cobra's sorted `Commands()` order.
        for index in 0..nodes.len() {
            if index == root {
                continue;
            }
            let mut children = std::mem::take(&mut nodes[index].children);
            children.sort_by(|left, right| nodes[*left].name.cmp(&nodes[*right].name));
            nodes[index].children = children;
        }
        Self {
            nodes,
            root,
            by_path,
        }
    }

    pub fn node(&self, index: usize) -> &Node {
        &self.nodes[index]
    }

    pub fn by_path(&self, path: &str) -> Option<usize> {
        self.by_path.get(path).copied()
    }

    /// `symdesk ls` -> `ls`; the root -> ``. The key of the recorded help text.
    pub fn help_key(&self, index: usize) -> &str {
        let path = &self.nodes[index].path;
        path.strip_prefix("symdesk").unwrap_or(path).trim_start()
    }

    /// Sorted children (Cobra's `Commands()`).
    pub fn sorted_children(&self, index: usize) -> Vec<usize> {
        let mut children = self.nodes[index].children.clone();
        children.sort_by(|left, right| self.nodes[*left].name.cmp(&self.nodes[*right].name));
        children
    }

    pub fn find_next(&self, index: usize, word: &str) -> Option<usize> {
        self.nodes[index].children.iter().copied().find(|child| {
            let node = &self.nodes[*child];
            node.name == word || node.aliases.iter().any(|alias| alias == word)
        })
    }

    pub fn is_available(&self, index: usize) -> bool {
        let node = &self.nodes[index];
        if node.hidden {
            return false;
        }
        if node.parent.is_some_and(|parent| {
            self.nodes[parent].name == "symdesk" && node.name == "help" && parent == self.root
        }) || (node.parent == Some(self.root) && node.name == "help")
        {
            return false;
        }
        node.runnable || self.has_available_subcommands(index)
    }

    pub fn has_available_subcommands(&self, index: usize) -> bool {
        self.nodes[index]
            .children
            .iter()
            .any(|child| self.is_available(*child))
    }

    pub fn has_subcommands(&self, index: usize) -> bool {
        !self.nodes[index].children.is_empty()
    }

    /// `c.Flags()` after `mergePersistentFlags`: local, own persistent, then
    /// the persistent flags of every ancestor. `execute` adds `--help` (and
    /// the root's `--version`); `Find` runs before either exists.
    pub fn flag_set(&self, index: usize, execute: bool) -> FlagSet {
        let mut flags: Vec<Flag> = self.nodes[index].local.clone();
        flags.sort_by(|left, right| left.name.cmp(&right.name));
        let mut set = FlagSet::default();
        for flag in flags {
            set.add(flag);
        }
        for flag in &self.nodes[index].persistent {
            set.add_if_absent(flag.clone());
        }
        let mut cursor = self.nodes[index].parent;
        while let Some(parent) = cursor {
            for flag in &self.nodes[parent].persistent {
                set.add_if_absent(flag.clone());
            }
            cursor = self.nodes[parent].parent;
        }
        if execute {
            set.add_if_absent(Flag::help(&self.nodes[index].name_for_help()));
            if index == self.root {
                set.add_if_absent(Flag::version(&self.nodes[index].name));
            }
        }
        set
    }

    /// Flags the command defines for itself (its own local + persistent),
    /// separated from the inherited ones the way `InheritedFlags()` does.
    pub fn inherited_flags(&self, index: usize) -> Vec<Flag> {
        let mut inherited = Vec::new();
        let mut cursor = self.nodes[index].parent;
        while let Some(parent) = cursor {
            for flag in &self.nodes[parent].persistent {
                let own = self.nodes[index]
                    .local
                    .iter()
                    .chain(&self.nodes[index].persistent)
                    .any(|own| own.name == flag.name);
                if !own && !inherited.iter().any(|seen: &Flag| seen.name == flag.name) {
                    inherited.push(flag.clone());
                }
            }
            cursor = self.nodes[parent].parent;
        }
        inherited.sort_by(|left, right| left.name.cmp(&right.name));
        inherited
    }
}

impl Node {
    fn name_for_help(&self) -> String {
        self.name.clone()
    }
}

fn builtin_for(words: &[&str]) -> Builtin {
    match words {
        ["symdesk", "help"] => Builtin::Help,
        ["symdesk", "completion", "bash"] => Builtin::Completion("bash"),
        ["symdesk", "completion", "zsh"] => Builtin::Completion("zsh"),
        ["symdesk", "completion", "fish"] => Builtin::Completion("fish"),
        ["symdesk", "completion", "powershell"] => Builtin::Completion("powershell"),
        _ => Builtin::Native,
    }
}

impl FlagSpec {
    fn into_flag(self) -> Flag {
        let kind = match self.kind.as_str() {
            "bool" => FlagKind::Bool,
            "string" => FlagKind::String,
            "int" => FlagKind::Int,
            "int64" => FlagKind::Int,
            "duration" => FlagKind::Duration,
            "stringArray" => FlagKind::StringArray,
            other => panic!("unsupported flag type {other}"),
        };
        let exclusive = self
            .annotations
            .get(MUTUALLY_EXCLUSIVE)
            .cloned()
            .unwrap_or_default();
        Flag {
            name: self.name,
            shorthand: self.shorthand.and_then(|value| value.bytes().next()),
            usage: self.usage,
            kind,
            default: self.default,
            no_opt_default: self.no_opt_default.filter(|value| !value.is_empty()),
            hidden: self.hidden,
            one_required: self
                .annotations
                .get(ONE_REQUIRED)
                .is_some_and(|values| values.first().is_some_and(|value| value == "true")),
            exclusive_groups: exclusive,
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[derive(Deserialize)]
    struct Document {
        commands: Vec<Recorded>,
    }

    #[derive(Deserialize)]
    struct Recorded {
        path: String,
        #[serde(default)]
        argument_count_probes: Vec<RecordedProbe>,
    }

    #[derive(Deserialize)]
    struct RecordedProbe {
        count: usize,
        accepted: bool,
        #[serde(default)]
        error: String,
    }

    fn recorded() -> Document {
        serde_json::from_str(include_str!(
            "../../../../testdata/port/cli/symdesk-command-tree.json"
        ))
        .expect("tree")
    }

    #[test]
    fn tree_has_every_recorded_path() {
        let tree = Tree::get();
        let document = recorded();
        assert_eq!(document.commands.len(), 207);
        for command in &document.commands {
            let index = tree
                .by_path(&command.path)
                .unwrap_or_else(|| panic!("{}", command.path));
            assert_eq!(tree.node(index).path, command.path);
        }
        // 207 recorded nodes plus the hidden `__complete` request command.
        assert_eq!(tree.nodes.len(), 208);
        assert_eq!(tree.nodes.iter().filter(|node| node.hidden).count(), 4);
    }

    #[test]
    fn argument_validators_reproduce_every_recorded_probe() {
        let tree = Tree::get();
        let mut checked = 0;
        for command in recorded().commands {
            let node = tree.node(tree.by_path(&command.path).unwrap());
            for probe in &command.argument_count_probes {
                let args: Vec<String> = (1..=probe.count).map(|n| format!("arg-{n}")).collect();
                let message = node.arity.check(&node.path, &args);
                assert_eq!(
                    message.is_none(),
                    probe.accepted,
                    "{} x{}",
                    command.path,
                    probe.count
                );
                if let Some(message) = message {
                    assert_eq!(message, probe.error, "{} x{}", command.path, probe.count);
                }
                checked += 1;
            }
        }
        assert_eq!(checked, 1330);
    }

    #[test]
    fn root_registration_order_matches_the_recorded_children() {
        let tree = Tree::get();
        let names: Vec<&str> = tree
            .node(tree.root)
            .children
            .iter()
            .map(|child| tree.node(*child).name.as_str())
            .collect();
        assert_eq!(names.len(), 51);
        assert_eq!(&names[48..], ["help", "__complete", "completion"]);
        assert_eq!(names[0], "version");
    }
}
