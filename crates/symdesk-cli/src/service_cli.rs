//! Shared plumbing of the service-backed commands (Go `initServiceDeps` +
//! `closeWithWarning`): resolve the vault, open its sidecar, run the body and
//! close the sidecar, reporting failures the way Go's `main` does.
use std::{path::Path, process::ExitCode};

use symdesk_index::{Sidecar, open_for_vault};

use crate::{emit_error, resolve_vault};

/// Go `initServiceDeps` error wording for an unusable vault selection.
pub fn vault_error(vault: Option<&str>, error: &str) -> String {
    if error.starts_with("vault path not configured") {
        "vault path not configured (use flag or SYMDESK_VAULT env)".to_owned()
    } else {
        crate::index_cli::index_vault_error(vault, error)
    }
}

/// Runs `body` with the resolved vault root and its open sidecar.
pub fn with_deps(
    vault: Option<&str>,
    output_json: bool,
    body: impl FnOnce(&Path, &mut Sidecar) -> Result<ExitCode, String>,
) -> ExitCode {
    let result = (|| -> Result<ExitCode, String> {
        let root = resolve_vault(vault).map_err(|error| vault_error(vault, &error))?;
        let mut sidecar = open_for_vault(&root).map_err(|error| error.to_string())?;
        let result = body(&root, &mut sidecar);
        if let Err(error) = sidecar.close() {
            eprintln!("warning: failed to close sidecar database: {error}");
        }
        result
    })();
    match result {
        Ok(exit) => exit,
        Err(error) => emit_error(error, output_json),
    }
}

/// Go `fmt` `%v` of a `map[string]string`/`map[string]interface{}` of strings:
/// `map[k:v k2:v2]` in key order.
pub fn go_string_map<'a>(entries: impl IntoIterator<Item = (&'a String, &'a String)>) -> String {
    let rendered: Vec<String> = entries
        .into_iter()
        .map(|(key, value)| format!("{key}:{value}"))
        .collect();
    format!("map[{}]", rendered.join(" "))
}
