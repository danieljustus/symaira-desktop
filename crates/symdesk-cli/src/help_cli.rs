//! Cobra's generated documents are release assets, captured from the real Go
//! process for every node of the frozen tree. They are not Clap templates.
//! Which document a command line asks for is decided by `crate::cobra`, which
//! also reproduces Cobra's flag parsing, never initializing a vault for help.
use std::{collections::BTreeMap, ffi::OsString, process::ExitCode, sync::OnceLock};

use serde::Deserialize;

#[derive(Deserialize)]
struct Documents {
    help: BTreeMap<String, String>,
    completion: BTreeMap<String, String>,
    root_usage: String,
}

fn documents() -> &'static Documents {
    static DOCUMENTS: OnceLock<Documents> = OnceLock::new();
    DOCUMENTS.get_or_init(|| {
        let mut documents: Documents =
            serde_json::from_str(include_str!("../../../testdata/port/cli/cobra-help.json"))
                .expect("Go-generated Cobra documents");
        // The recording stays as it is; commands that came after the Go oracle
        // are inserted from the reviewed table (docs/rust-port/cli-additions.md).
        crate::cli_additions::apply(
            &mut documents.help,
            &mut documents.root_usage,
            crate::cli_additions::declared(),
        )
        .expect("declared CLI additions apply to the recorded documents");
        documents
    })
}

/// `help <topics>` for a command declared in `assets/cli-additions.json`, or
/// `None` when the topics name no added command (Cobra's own answer applies).
pub fn added_help(topics: &[String]) -> Option<ExitCode> {
    let (addition, consumed) =
        crate::cli_additions::find_help(crate::cli_additions::declared(), topics)?;
    match &addition.help {
        crate::cli_additions::Help::Text { text } => {
            (consumed == topics.len()).then(|| crate::write_stdout(text.clone()))
        }
        // The journal answers for itself: `help room note` is `room note --help`.
        crate::cli_additions::Help::Symroom => Some(symroom_cli::run(
            std::iter::once(OsString::from("symroom"))
                .chain(topics[consumed..].iter().map(OsString::from))
                .chain(std::iter::once(OsString::from("--help"))),
        )),
    }
}

pub fn render(path: &str) -> ExitCode {
    crate::write_stdout(documents().help[path].clone())
}

/// `completion <shell>` output; `key` is `bash` or `bash-no-descriptions`.
pub fn completion_script(key: &str) -> Option<&'static str> {
    documents().completion.get(key).map(String::as_str)
}

/// The root usage printed after `Unknown help topic`.
pub fn root_usage() -> &'static str {
    documents().root_usage.as_str()
}
