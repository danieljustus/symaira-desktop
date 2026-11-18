//! Post-oracle CLI additions (docs/rust-port/cli-additions.md): the product's
//! help documents are the Go recording plus exactly the declared additions.
//! The recording is never edited; these tests keep both sides honest.
#[path = "../../test-support/process_env.rs"]
mod process_env;
use process_env::IsolatedEnvironment;

#[path = "../src/cli_additions.rs"]
#[allow(dead_code)]
mod cli_additions;
use cli_additions::{Addition, Help};

use serde::Deserialize;
use std::{
    collections::BTreeMap,
    fs,
    path::{Path, PathBuf},
    process::Command,
    time::{SystemTime, UNIX_EPOCH},
};

/// Every addition the product declares. A new command shows up here in the
/// diff: (parent help key, name, group heading, short description).
const DECLARED: &[(&str, &str, &str, &str)] = &[(
    "",
    "room",
    "Maintenance Commands",
    "Project journal: signed rooms, notes, decisions and runs",
)];

const COBRA_HELP: &str = include_str!("../../../testdata/port/cli/cobra-help.json");
const COMMAND_TREE: &str = include_str!("../../../testdata/port/cli/symdesk-command-tree.json");
/// Names Cobra reserves or generates without listing them in the recorded tree.
const RESERVED: &[&str] = &["help", "completion", "__complete", "__completeNoDesc"];

#[derive(Deserialize)]
struct Frozen {
    help: BTreeMap<String, String>,
    completion: BTreeMap<String, String>,
    root_usage: String,
}

#[derive(Deserialize)]
struct Tree {
    commands: Vec<TreeNode>,
}

#[derive(Deserialize)]
struct TreeNode {
    path: String,
    #[serde(default)]
    aliases: Vec<String>,
}

fn frozen() -> Frozen {
    serde_json::from_str(COBRA_HELP).expect("Go-recorded documents")
}

fn add(parent: &str, name: &str, group: &str, short: &str) -> Addition {
    Addition {
        parent: parent.to_owned(),
        name: name.to_owned(),
        group: group.to_owned(),
        short: short.to_owned(),
        help: Help::Text {
            text: format!("{short}\n"),
        },
    }
}

/// Cobra's list line: name padded to 11, one space, short.
fn line(name: &str, short: &str) -> String {
    format!("  {name:<11} {short}")
}

fn declared_lines() -> Vec<String> {
    DECLARED
        .iter()
        .map(|(_, name, _, short)| line(name, short))
        .collect()
}

#[test]
fn the_declared_additions_are_exactly_the_reviewed_list() {
    let actual: Vec<(&str, &str, &str, &str)> = cli_additions::declared()
        .iter()
        .map(|a| {
            (
                a.parent.as_str(),
                a.name.as_str(),
                a.group.as_str(),
                a.short.as_str(),
            )
        })
        .collect();
    assert_eq!(actual, DECLARED);
}

#[test]
fn declared_additions_never_shadow_a_frozen_command_or_key() {
    let frozen = frozen();
    let tree: Tree = serde_json::from_str(COMMAND_TREE).expect("Go-recorded tree");
    for a in cli_additions::declared() {
        assert!(
            frozen.help.contains_key(&a.parent),
            "{}: parent is not a frozen help key",
            a.name
        );
        let key = if a.parent.is_empty() {
            a.name.clone()
        } else {
            format!("{} {}", a.parent, a.name)
        };
        assert!(!frozen.help.contains_key(&key), "{key} is a frozen key");
        let prefix = if a.parent.is_empty() {
            "symdesk ".to_owned()
        } else {
            format!("symdesk {} ", a.parent)
        };
        for node in &tree.commands {
            assert_ne!(node.path, format!("{prefix}{}", a.name), "{key} is frozen");
            if let Some(rest) = node.path.strip_prefix(&prefix)
                && !rest.contains(' ')
            {
                assert!(
                    !node.aliases.contains(&a.name),
                    "{key} is an alias of {}",
                    node.path
                );
            }
        }
        assert!(!RESERVED.contains(&a.name.as_str()), "{key} is reserved");
        let heading = format!("{}:", a.group);
        assert!(
            frozen.help[&a.parent].lines().any(|l| l == heading),
            "{key}: no `{heading}` list in the frozen `{}` document",
            a.parent
        );
    }
}

#[test]
fn shadowing_or_malformed_additions_are_rejected() {
    let frozen = frozen();
    let root = &frozen.help[""];
    for (parent, name, group) in [
        ("", "ls", "Vault Commands"),        // existing command
        ("", "ls", "Maintenance Commands"),  // existing, other group
        ("", "help", "Additional Commands"), // generated command
        ("meeting", "list", "Available Commands"),
        ("", "room", "No Such Commands"), // missing list
    ] {
        let doc = &frozen.help[parent];
        let result = cli_additions::augment(parent, doc, &[add(parent, name, group, "x")]);
        assert!(result.is_err(), "{parent}/{name}/{group} must be rejected");
    }
    // A parent that is not a frozen document.
    let mut help = frozen.help.clone();
    let mut usage = frozen.root_usage.clone();
    let result = cli_additions::apply(
        &mut help,
        &mut usage,
        &[add("no such node", "x", "Available Commands", "x")],
    );
    assert!(result.is_err());
    // Malformed declarations never load.
    for json in [
        r#"{"schema_version":2,"additions":[]}"#,
        r#"{"schema_version":1,"additions":[{"parent":"","name":"a b","group":"G Commands","short":"s","help":{"source":"symroom"}}]}"#,
        r#"{"schema_version":1,"additions":[{"parent":"","name":"-x","group":"G Commands","short":"s","help":{"source":"symroom"}}]}"#,
        r#"{"schema_version":1,"additions":[{"parent":"","name":"x","group":"G Commands","short":"","help":{"source":"symroom"}}]}"#,
        r#"{"schema_version":1,"additions":[{"parent":"meeting","name":"x","group":"Available Commands","short":"s","help":{"source":"symroom"}}]}"#,
        r#"{"schema_version":1,"additions":[{"parent":"","name":"x","group":"G Commands","short":"s","help":{"source":"text","text":"no newline"}}]}"#,
        r#"{"schema_version":1,"additions":[{"parent":"","name":"x","group":"G Commands","short":"s","help":{"source":"symroom"},"extra":1}]}"#,
    ] {
        assert!(cli_additions::parse(json).is_err(), "accepted: {json}");
    }
    assert!(root.contains("Maintenance Commands:"));
}

#[test]
fn a_subcommand_under_a_frozen_node_is_listed_in_order_and_repadded() {
    let frozen = frozen();
    let meeting = &frozen.help["meeting"];
    let import = add(
        "meeting",
        "import",
        "Available Commands",
        "Import a meeting folder",
    );
    let produced = cli_additions::augment("meeting", meeting, &[import]).expect("insert");
    let expected = meeting.replace(
        "  list        List imported",
        &format!(
            "{}\n  list        List imported",
            line("import", "Import a meeting folder")
        ),
    );
    assert_ne!(expected, *meeting);
    assert_eq!(produced, expected);
    // A name longer than Cobra's minimum padding widens every entry.
    let wide = add(
        "meeting",
        "segments-of-a-meeting",
        "Available Commands",
        "Wide",
    );
    let produced = cli_additions::augment("meeting", meeting, &[wide]).expect("insert");
    let width = "segments-of-a-meeting".len();
    for entry in ["list", "participant", "publish", "review", "show"] {
        let before = meeting
            .lines()
            .find(|l| l.starts_with(&format!("  {entry} ")))
            .expect("frozen entry");
        let shorts = before
            .split_whitespace()
            .skip(1)
            .collect::<Vec<_>>()
            .join(" ");
        let after = produced
            .lines()
            .find(|l| l.starts_with(&format!("  {entry} ")))
            .expect("entry kept");
        assert_eq!(after, format!("  {entry:<width$} {shorts}"));
    }
    assert!(produced.contains(&format!("  {:<width$} Wide\n", "segments-of-a-meeting")));
    // Everything that is not part of a list stays as recorded.
    let strip = |text: &str| {
        text.lines()
            .filter(|l| !l.starts_with("  ") || l.starts_with("  -"))
            .collect::<Vec<_>>()
            .join("\n")
    };
    assert_eq!(strip(&produced), strip(meeting));
}

#[test]
fn text_help_of_an_added_subcommand_is_found_by_its_path() {
    let import = add("meeting", "import", "Available Commands", "Import");
    let room = cli_additions::declared()
        .iter()
        .find(|a| a.name == "room")
        .expect("room")
        .clone();
    let additions = [import.clone(), room.clone()];
    let words = |text: &str| text.split(' ').map(str::to_owned).collect::<Vec<_>>();
    let found = cli_additions::find_help(&additions, &words("meeting import")).expect("found");
    assert_eq!((found.0, found.1), (&import, 2));
    let found = cli_additions::find_help(&additions, &words("room note")).expect("found");
    assert_eq!((found.0, found.1), (&room, 1));
    assert!(cli_additions::find_help(&additions, &words("meeting")).is_none());
    assert!(cli_additions::find_help(&additions, &words("meeting list")).is_none());
    assert!(cli_additions::find_help(&additions, &words("ls")).is_none());
}

#[test]
fn untouched_documents_are_byte_identical_and_touched_ones_differ_by_declared_lines_only() {
    let frozen = frozen();
    let mut help = frozen.help.clone();
    let mut usage = frozen.root_usage.clone();
    cli_additions::apply(&mut help, &mut usage, cli_additions::declared()).expect("apply");
    let parents: Vec<&str> = DECLARED.iter().map(|d| d.0).collect();
    let lines = declared_lines();
    assert_eq!(help.len(), frozen.help.len(), "no key added or removed");
    let mut touched = 0;
    for (key, doc) in &frozen.help {
        if parents.contains(&key.as_str()) {
            touched += 1;
            let kept: Vec<&str> = help[key]
                .split('\n')
                .filter(|l| !lines.iter().any(|x| x == l))
                .collect();
            assert_eq!(
                kept.join("\n"),
                *doc,
                "{key}: more than the declared lines changed"
            );
            assert_eq!(
                help[key].split('\n').count(),
                doc.split('\n').count() + lines.len()
            );
        } else {
            assert_eq!(
                &help[key], doc,
                "{key}: not an addition target, must stay frozen"
            );
        }
    }
    assert_eq!(touched, 1);
    let kept: Vec<&str> = usage
        .split('\n')
        .filter(|l| !lines.iter().any(|x| x == l))
        .collect();
    assert_eq!(kept.join("\n"), frozen.root_usage);
    // The root document gets room between `retention` and `sidecar`, nowhere else.
    let anchor = "  retention   Evaluate and apply document retention rules\n";
    let room = format!(
        "{}\n",
        line(
            "room",
            "Project journal: signed rooms, notes, decisions and runs"
        )
    );
    for (frozen_doc, product) in [(&frozen.help[""], &help[""]), (&frozen.root_usage, &usage)] {
        assert_eq!(
            *product,
            frozen_doc.replace(anchor, &format!("{anchor}{room}"))
        );
        assert_ne!(product, frozen_doc);
    }
}

fn scratch() -> PathBuf {
    let root = std::env::temp_dir().join(format!(
        "symdesk-additions-{}-{}",
        std::process::id(),
        SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    ));
    fs::create_dir_all(&root).unwrap();
    root
}

struct Run {
    code: Option<i32>,
    stdout: Vec<u8>,
    stderr: Vec<u8>,
}

fn run(root: &Path, args: &[&str]) -> Run {
    let output = Command::new(env!("CARGO_BIN_EXE_symdesk"))
        .current_dir(root)
        .isolated_env()
        .env("HOME", root)
        .env("USERPROFILE", root)
        .env("XDG_CONFIG_HOME", root.join("config"))
        .env("XDG_DATA_HOME", root.join("data"))
        .env("XDG_CACHE_HOME", root.join("cache"))
        .env("TZ", "UTC")
        .env("LANG", "C")
        .env("LC_ALL", "C")
        .env("PATH", root)
        .args(args)
        .output()
        .expect("run symdesk");
    Run {
        code: output.status.code(),
        stdout: output.stdout,
        stderr: output.stderr,
    }
}

#[test]
fn product_help_is_the_recording_plus_the_declared_additions() {
    let frozen = frozen();
    let root = scratch();
    let lines = declared_lines();
    let parents: Vec<&str> = DECLARED.iter().map(|d| d.0).collect();
    let mut checked = 0;
    for (key, doc) in &frozen.help {
        let words: Vec<&str> = key.split(' ').filter(|w| !w.is_empty()).collect();
        let by_help = [&["help"][..], &words].concat();
        let by_flag = [&words[..], &["--help"][..]].concat();
        for args in [by_help, by_flag] {
            let observed = run(&root, &args);
            let stdout = String::from_utf8(observed.stdout).expect("utf-8");
            assert_eq!(observed.code, Some(0), "{args:?} exit");
            if parents.contains(&key.as_str()) {
                // Only the declared lines may differ from the recording.
                let kept: Vec<&str> = stdout
                    .split('\n')
                    .filter(|l| !lines.iter().any(|x| x == l))
                    .collect();
                // The root document of `--help` carries the program's own `-h` and
                // `-v` flag lines, which the recording has too: compare whole.
                assert_eq!(kept.join("\n"), *doc, "{args:?}");
                for declared in &lines {
                    assert_eq!(stdout.matches(declared.as_str()).count(), 1, "{args:?}");
                }
            } else {
                assert_eq!(stdout, *doc, "{args:?}: must equal the Go recording");
            }
            assert!(observed.stderr.is_empty(), "{args:?} stderr");
            checked += 1;
        }
    }
    assert_eq!(checked, 2 * frozen.help.len());
    // Bare `symdesk` prints the root document, `help <unknown>` the root usage.
    let bare = run(&root, &[]);
    let bare = String::from_utf8(bare.stdout).unwrap();
    assert!(declared_lines().iter().all(|l| bare.contains(l.as_str())));
    let unknown = run(&root, &["help", "not-a-command"]);
    let stderr = String::from_utf8(unknown.stderr).unwrap();
    let kept: Vec<&str> = stderr
        .split('\n')
        .filter(|l| !lines.iter().any(|x| x == l))
        .collect();
    assert_eq!(
        kept.join("\n"),
        format!(
            "Unknown help topic [`not-a-command`]\n{}",
            frozen.root_usage
        )
    );
    fs::remove_dir_all(root).unwrap();
}

#[test]
fn completion_scripts_stay_the_go_recording() {
    // Cobra's generated scripts are generic and dynamic: they contain no
    // command name, so an addition cannot appear in them and they stay frozen.
    let frozen = frozen();
    let root = scratch();
    assert_eq!(frozen.completion.len(), 8);
    for (key, script) in &frozen.completion {
        let shell = key.trim_end_matches("-no-descriptions");
        let mut args = vec!["completion", shell];
        if key.ends_with("-no-descriptions") {
            args.push("--no-descriptions");
        }
        let observed = run(&root, &args);
        assert_eq!(observed.code, Some(0), "{key}");
        assert_eq!(observed.stdout, script.as_bytes(), "{key}");
        for (_, name, _, _) in DECLARED {
            assert!(!script.contains(&format!(" {name} ")), "{key}");
        }
    }
    fs::remove_dir_all(root).unwrap();
}

#[test]
fn help_room_prints_the_journals_own_usage() {
    let root = scratch();
    for rest in [&[][..], &["note"][..], &["verify"][..]] {
        let by_help = run(&root, &[&["help", "room"][..], rest].concat());
        let direct = run(&root, &[&["room"][..], rest, &["--help"][..]].concat());
        assert_eq!(by_help.stdout, direct.stdout, "{rest:?}");
        assert_eq!(by_help.stderr, direct.stderr, "{rest:?}");
        assert_eq!(by_help.code, direct.code, "{rest:?}");
        // The journal prints a subcommand's flag usage on stderr.
        assert!(
            !(direct.stdout.is_empty() && direct.stderr.is_empty()),
            "{rest:?}"
        );
    }
    let usage = run(&root, &["help", "room"]);
    assert_eq!(usage.code, Some(0));
    assert!(
        usage
            .stdout
            .starts_with(b"symroom - room management and coordination tool")
    );
    fs::remove_dir_all(root).unwrap();
}
