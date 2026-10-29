//! Go: `cmd/symdesk/duplicates.go` and `similar.go`.
//!
//! `duplicates` lists SimHash near-duplicate groups; `duplicates similar <file>`
//! (and the hidden top-level `similar <file>`) list one file's near-duplicates.

use std::{
    fs,
    path::{Path, PathBuf},
    process::ExitCode,
};

use clap::{Arg, ArgMatches, Command};
use symdesk_index::{DuplicateGroup, SimilarResult, open_for_vault, resolve_duplicate_threshold};
use symdesk_vault::{go_path_error, go_quote, parse_bytes, secure_path};

use crate::{emit_error, resolve_vault, write_go_json, write_stdout};

const MAX_ROOT_READ_BYTES: u64 = 64 << 20;
const NO_VAULT_ERROR: &str = "vault path not configured (use flag or SYMDESK_VAULT env)";
const THRESHOLD_HELP: &str = "minimum similarity percentage (0-100)";

fn threshold_arg() -> Arg {
    Arg::new("threshold")
        .long("threshold")
        .help(THRESHOLD_HELP)
        .num_args(1)
        .allow_hyphen_values(true)
        .overrides_with("threshold")
}

fn similar_command(name: &'static str) -> Command {
    Command::new(name)
        .about("Find near-duplicate documents by SimHash similarity")
        .arg(Arg::new("file").num_args(0..).allow_hyphen_values(false))
        .arg(threshold_arg())
}

pub fn duplicates_cli() -> Command {
    Command::new("duplicates")
        .about("List groups of possible duplicate documents (SimHash)")
        .arg(Arg::new("extra").num_args(0..))
        .arg(threshold_arg())
        .subcommand(similar_command("similar"))
}

/// The retired top-level `similar`, still registered (hidden) for scripts.
pub fn similar_cli() -> Command {
    similar_command("similar").hide(true)
}

pub fn run_duplicates(command: &ArgMatches, vault: Option<&str>, output_json: bool) -> ExitCode {
    if let Some(("similar", subcommand)) = command.subcommand() {
        return run_similar(subcommand, vault, output_json);
    }
    let extra = values(command, "extra");
    if let Some(first) = extra.first() {
        return emit_error(
            format!(
                "unknown command {} for \"symdesk duplicates\"",
                go_quote(first)
            ),
            output_json,
        );
    }
    let threshold = match parse_threshold(command) {
        Ok(value) => value,
        Err(error) => return emit_error(error, output_json),
    };
    let vault_root = match service_vault(vault) {
        Ok(root) => root,
        Err(error) => return emit_error(error, output_json),
    };
    let mut sidecar = match open_for_vault(&vault_root) {
        Ok(sidecar) => sidecar,
        Err(error) => return emit_error(error.to_string(), output_json),
    };
    let result = match sidecar.similar_all(&vault_root, threshold.unwrap_or(0)) {
        Ok(groups) if output_json => write_go_json(&groups),
        Ok(groups) => write_stdout(format!("{}\n", format_groups(&groups))),
        Err(error) => emit_error(error.to_string(), output_json),
    };
    close_sidecar(&mut sidecar);
    result
}

pub fn run_similar(command: &ArgMatches, vault: Option<&str>, output_json: bool) -> ExitCode {
    let files = values(command, "file");
    if files.len() != 1 {
        return emit_error(
            format!("accepts 1 arg(s), received {}", files.len()),
            output_json,
        );
    }
    let threshold = match parse_threshold(command) {
        Ok(value) => value,
        Err(error) => return emit_error(error, output_json),
    };
    let vault_root = match service_vault(vault) {
        Ok(root) => root,
        Err(error) => return emit_error(error, output_json),
    };
    let mut sidecar = match open_for_vault(&vault_root) {
        Ok(sidecar) => sidecar,
        Err(error) => return emit_error(error.to_string(), output_json),
    };
    let result = (|| -> Result<Vec<SimilarResult>, String> {
        let reference = reference_simhash(&vault_root, &files[0])?;
        sidecar
            .similar_docs(
                &reference,
                resolve_duplicate_threshold(threshold.unwrap_or(0)),
            )
            .map_err(|error| error.to_string())
    })();
    let exit = match result {
        // Go leaves a no-match result as a nil slice: JSON `null`, text `[]`.
        Ok(results) if output_json && results.is_empty() => write_stdout("null\n".to_owned()),
        Ok(results) if output_json => write_go_json(&results),
        Ok(results) => write_stdout(format!("{}\n", format_similar(&results))),
        Err(error) => emit_error(error, output_json),
    };
    close_sidecar(&mut sidecar);
    exit
}

/// Go's `vault.ResolveVaultRoot` wording for the service-backed commands,
/// which differs from the `ls`/`search` selection path.
fn service_vault(vault: Option<&str>) -> Result<PathBuf, String> {
    resolve_vault(vault).map_err(|error| {
        if error.starts_with("vault path not configured") {
            NO_VAULT_ERROR.to_owned()
        } else {
            crate::index_cli::index_vault_error(vault, &error)
        }
    })
}

fn close_sidecar(sidecar: &mut symdesk_index::Sidecar) {
    if let Err(error) = sidecar.close() {
        eprintln!("warning: failed to close sidecar database: {error}");
    }
}

fn values(command: &ArgMatches, name: &str) -> Vec<String> {
    command
        .get_many::<String>(name)
        .map(|values| values.cloned().collect())
        .unwrap_or_default()
}

fn parse_threshold(command: &ArgMatches) -> Result<Option<i64>, String> {
    let Some(raw) = command.get_one::<String>("threshold") else {
        return Ok(None);
    };
    parse_go_int(raw).map(Some).map_err(|reason| {
        format!(
            "invalid argument {} for \"--threshold\" flag: strconv.ParseInt: parsing {}: {reason}",
            go_quote(raw),
            go_quote(raw),
        )
    })
}

/// Go's `strconv.ParseInt(s, 0, 64)` as used by pflag's `Int` flag; the error
/// is pflag's rewritten reason text.
pub(crate) fn parse_go_int(raw: &str) -> Result<i64, &'static str> {
    const SYNTAX: &str = "invalid syntax";
    const RANGE: &str = "value out of range";
    let (negative, digits) = match raw.as_bytes().first() {
        Some(b'+') => (false, &raw[1..]),
        Some(b'-') => (true, &raw[1..]),
        _ => (false, raw),
    };
    if digits.is_empty() {
        return Err(SYNTAX);
    }
    let bytes = digits.as_bytes();
    let (base, body): (u64, &[u8]) = if bytes[0] == b'0' {
        match bytes.get(1).map(u8::to_ascii_lowercase) {
            Some(b'b') if bytes.len() >= 3 => (2, &bytes[2..]),
            Some(b'o') if bytes.len() >= 3 => (8, &bytes[2..]),
            Some(b'x') if bytes.len() >= 3 => (16, &bytes[2..]),
            _ => (8, &bytes[1..]),
        }
    } else {
        (10, bytes)
    };
    let cutoff = u64::MAX / base + 1;
    let mut value = 0_u64;
    let mut underscores = false;
    for &byte in body {
        let digit = match byte {
            b'_' => {
                underscores = true;
                continue;
            }
            b'0'..=b'9' => u64::from(byte - b'0'),
            b'a'..=b'z' => u64::from(byte - b'a') + 10,
            b'A'..=b'Z' => u64::from(byte - b'A') + 10,
            _ => return Err(SYNTAX),
        };
        if digit >= base {
            return Err(SYNTAX);
        }
        if value >= cutoff {
            return Err(RANGE);
        }
        value *= base;
        match value.checked_add(digit) {
            Some(next) => value = next,
            None => return Err(RANGE),
        }
    }
    if underscores && !underscore_ok(bytes) {
        return Err(SYNTAX);
    }
    let limit = 1_u64 << 63;
    if (!negative && value >= limit) || (negative && value > limit) {
        return Err(RANGE);
    }
    Ok(if negative {
        (value as i64).wrapping_neg()
    } else {
        value as i64
    })
}

/// Go's `underscoreOK`: underscores only separate digits (or follow a base prefix).
fn underscore_ok(text: &[u8]) -> bool {
    let mut index = 0;
    let mut saw = b'^';
    let mut hex = false;
    if text.len() >= 2
        && text[0] == b'0'
        && matches!(text[1].to_ascii_lowercase(), b'b' | b'o' | b'x')
    {
        index = 2;
        saw = b'0';
        hex = text[1].eq_ignore_ascii_case(&b'x');
    }
    while index < text.len() {
        let byte = text[index];
        index += 1;
        if byte.is_ascii_digit() || (hex && matches!(byte.to_ascii_lowercase(), b'a'..=b'f')) {
            saw = b'0';
            continue;
        }
        if byte == b'_' {
            if saw != b'0' {
                return false;
            }
            saw = b'_';
            continue;
        }
        if saw == b'_' {
            return false;
        }
        saw = b'!';
    }
    saw != b'_'
}

/// Go's `SimilarDocs` front half: confine the path, read it through the vault
/// root and return the stored or freshly computed simhash.
pub(crate) fn reference_simhash(vault_root: &Path, requested: &str) -> Result<String, String> {
    let absolute = secure_path(vault_root, requested).map_err(|error| error.to_string())?;
    let canonical_root = fs::canonicalize(vault_root)
        .map_err(|error| format!("resolve canonical vault root: {error}"))?;
    let relative = absolute
        .strip_prefix(&canonical_root)
        .map_err(|_| format!("vault path escapes root: {}", absolute.display()))?;
    let shown = absolute.display();
    let metadata = fs::metadata(&absolute).map_err(|error| {
        format!(
            "open vault file {shown}: {}",
            go_path_error("openat", relative, &error)
        )
    })?;
    if !metadata.is_file() {
        return Err(format!("vault path is not a regular file: {shown}"));
    }
    if metadata.len() > MAX_ROOT_READ_BYTES {
        return Err(format!(
            "vault file exceeds {MAX_ROOT_READ_BYTES} byte read limit: {shown}"
        ));
    }
    let bytes = fs::read(&absolute).map_err(|error| {
        format!(
            "open vault file {shown}: {}",
            go_path_error("openat", relative, &error)
        )
    })?;
    let path = absolute
        .to_str()
        .ok_or_else(|| format!("non-UTF-8 vault path: {:?}", absolute))?;
    let document = parse_bytes(path, &bytes).map_err(|error| error.to_string())?;
    Ok(if document.simhash.is_empty() {
        symdesk_core::simhash::compute_hex(&document.body)
    } else {
        document.simhash
    })
}

/// Go's `%+v` for `[]service.DuplicateGroup`.
fn format_groups(groups: &[DuplicateGroup]) -> String {
    let rendered = groups
        .iter()
        .map(|group| {
            let members = group
                .members
                .iter()
                .map(|member| {
                    format!(
                        "{{Path:{} Title:{} Similarity:{}}}",
                        member.path, member.title, member.similarity
                    )
                })
                .collect::<Vec<_>>()
                .join(" ");
            format!(
                "{{Path:{} Title:{} Members:[{members}]}}",
                group.path, group.title
            )
        })
        .collect::<Vec<_>>()
        .join(" ");
    format!("[{rendered}]")
}

/// Go's `%+v` for `[]sidecar.SimilarResult` (the unexported-from-JSON `Body` is still printed).
fn format_similar(results: &[SimilarResult]) -> String {
    let rendered = results
        .iter()
        .map(|result| {
            format!(
                "{{Path:{} Title:{} Similarity:{} Simhash:{} Body:}}",
                result.path, result.title, result.similarity, result.simhash
            )
        })
        .collect::<Vec<_>>()
        .join(" ");
    format!("[{rendered}]")
}

#[cfg(test)]
mod tests {
    use super::{parse_go_int, underscore_ok};

    #[test]
    fn go_int_parsing_matches_strconv_base_zero() {
        for (input, expected) in [
            ("0", Ok(0)),
            ("85", Ok(85)),
            ("+5", Ok(5)),
            ("-5", Ok(-5)),
            ("0x10", Ok(16)),
            ("0X1f", Ok(31)),
            ("010", Ok(8)),
            ("0b11", Ok(3)),
            ("0o7", Ok(7)),
            ("1_0", Ok(10)),
            ("0_1", Ok(1)),
            ("0x_1", Ok(1)),
            ("-9223372036854775808", Ok(i64::MIN)),
            ("9223372036854775807", Ok(i64::MAX)),
            ("", Err("invalid syntax")),
            ("-", Err("invalid syntax")),
            ("08", Err("invalid syntax")),
            ("1.5", Err("invalid syntax")),
            (" 7", Err("invalid syntax")),
            ("1__0", Err("invalid syntax")),
            ("_1", Err("invalid syntax")),
            ("1_", Err("invalid syntax")),
            ("0x", Err("invalid syntax")),
            ("9223372036854775808", Err("value out of range")),
            ("-9223372036854775809", Err("value out of range")),
            ("99999999999999999999", Err("value out of range")),
            ("99999999999999999999x", Err("value out of range")),
        ] {
            assert_eq!(parse_go_int(input), expected, "{input:?}");
        }
    }

    #[test]
    fn underscore_rules_match_go() {
        assert!(underscore_ok(b"1_000"));
        assert!(underscore_ok(b"0x_ab"));
        assert!(!underscore_ok(b"_1"));
        assert!(!underscore_ok(b"1__1"));
        assert!(!underscore_ok(b"1_"));
    }
}
