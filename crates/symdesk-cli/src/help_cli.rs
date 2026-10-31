//! Cobra's generated documents are release assets, captured from the real Go
//! process for every node of the frozen tree. They are not Clap templates.
//! Which document a command line asks for is decided by `crate::cobra`, which
//! also reproduces Cobra's flag parsing, never initializing a vault for help.
use std::{collections::BTreeMap, process::ExitCode, sync::OnceLock};

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
        serde_json::from_str(include_str!("../../../testdata/port/cli/cobra-help.json"))
            .expect("Go-generated Cobra documents")
    })
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
