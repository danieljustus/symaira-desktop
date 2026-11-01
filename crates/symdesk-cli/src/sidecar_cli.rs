//! `symdesk sidecar list|prune` (Go `cmd/symdesk/sidecar.go`,
//! `internal/sidecar/maintenance.go`): inventory of the per-vault sidecars
//! below `<data home>/symdesk/vaults` and removal of the orphaned ones.
use std::{
    fs,
    path::{Path, PathBuf},
    process::ExitCode,
};

use clap::{Arg, ArgAction, ArgMatches, Command};
use serde::Serialize;

use crate::{emit_error, write_go_json, write_stdout};

pub fn cli() -> Command {
    Command::new("sidecar")
        .subcommand(Command::new("list").arg(Arg::new("extra").num_args(0..).hide(true)))
        .subcommand(
            Command::new("prune")
                .arg(Arg::new("extra").num_args(0..).hide(true))
                .arg(
                    Arg::new("confirm")
                        .long("confirm")
                        .action(ArgAction::Set)
                        .num_args(0..=1)
                        .require_equals(true)
                        .default_missing_value("true")
                        .default_value("false")
                        .overrides_with("confirm")
                        .value_parser(|value: &str| match value {
                            "1" | "t" | "T" | "true" | "TRUE" | "True" => Ok(true),
                            "0" | "f" | "F" | "false" | "FALSE" | "False" => Ok(false),
                            _ => Err("invalid boolean value"),
                        }),
                ),
        )
}

#[derive(Serialize)]
struct Entry {
    path: String,
    vault_path: String,
    last_used: String,
    size: u64,
    orphan: bool,
    metadata: bool,
    retrieval_index_path: String,
    retrieval_index_present: bool,
}

#[derive(Serialize)]
struct Report {
    sidecars: Vec<Entry>,
    count: usize,
    total_bytes: u64,
    orphan_count: usize,
    #[serde(skip_serializing_if = "String::is_empty")]
    legacy_path: String,
    legacy_present: bool,
    #[serde(skip_serializing_if = "String::is_empty")]
    legacy_retrieval_index_path: String,
    legacy_retrieval_index_present: bool,
}

const ZERO_TIME: &str = "0001-01-01T00:00:00Z";

/// Go `time.Time` JSON (RFC 3339 with nanoseconds, trailing zeros trimmed,
/// the original offset kept) for a recorded `last_used` string.
fn go_time_json(raw: &str) -> Option<String> {
    use time::format_description::well_known::Rfc3339;
    let parsed = time::OffsetDateTime::parse(raw, &Rfc3339).ok()?;
    let mut out = format!(
        "{:04}-{:02}-{:02}T{:02}:{:02}:{:02}",
        parsed.year(),
        u8::from(parsed.month()),
        parsed.day(),
        parsed.hour(),
        parsed.minute(),
        parsed.second()
    );
    let nanos = parsed.nanosecond();
    if nanos != 0 {
        let fraction = format!("{nanos:09}");
        out.push('.');
        out.push_str(fraction.trim_end_matches('0'));
    }
    let offset = parsed.offset();
    if offset.is_utc() {
        out.push('Z');
    } else {
        let (hours, minutes, _) = offset.as_hms();
        out.push_str(&format!(
            "{}{:02}:{:02}",
            if offset.whole_seconds() < 0 { '-' } else { '+' },
            hours.abs(),
            minutes.abs()
        ));
    }
    Some(out)
}

fn sidecar_root() -> Result<PathBuf, String> {
    let environment = symdesk_core::config::environment_snapshot()?;
    let data_home = environment
        .get("XDG_DATA_HOME")
        .map(|value| value.trim())
        .filter(|value| !value.is_empty())
        .map(PathBuf::from)
        .or_else(|| {
            environment
                .get(if cfg!(windows) { "USERPROFILE" } else { "HOME" })
                .filter(|value| !value.is_empty())
                .map(|home| Path::new(home).join(".local/share"))
        })
        .ok_or_else(|| {
            if cfg!(windows) {
                "user home dir: %userprofile% is not defined".to_owned()
            } else {
                "user home dir: $HOME is not defined".to_owned()
            }
        })?;
    Ok(data_home.join("symdesk").join("vaults"))
}

struct Listed {
    db_path: PathBuf,
    vault_path: String,
    last_used: String,
    size: u64,
    orphan: bool,
    metadata: bool,
}

fn list_sidecars() -> Result<Vec<Listed>, String> {
    let root = sidecar_root()?;
    let directories = match fs::read_dir(&root) {
        Ok(directories) => directories,
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => return Ok(Vec::new()),
        Err(error) => return Err(format!("read sidecar root: {}", os_error_text(&error))),
    };
    let mut names = directories
        .map(|entry| entry.map_err(|error| format!("read sidecar root: {error}")))
        .collect::<Result<Vec<_>, _>>()?;
    names.sort_by_key(fs::DirEntry::file_name);
    let mut result = Vec::new();
    for directory in names {
        if !directory.file_type().is_ok_and(|kind| kind.is_dir()) {
            continue;
        }
        let db_path = root.join(directory.file_name()).join("sidecar.db");
        let info = match fs::metadata(&db_path) {
            Ok(info) => info,
            Err(error) if error.kind() == std::io::ErrorKind::NotFound => continue,
            Err(error) => {
                return Err(format!(
                    "stat sidecar {}: {}",
                    db_path.display(),
                    os_error_text(&error)
                ));
            }
        };
        if !info.is_file() {
            continue;
        }
        let mut entry = Listed {
            db_path,
            vault_path: String::new(),
            last_used: ZERO_TIME.to_owned(),
            size: info.len(),
            orphan: false,
            metadata: false,
        };
        let metadata_path = root.join(directory.file_name()).join("metadata.json");
        if let Ok(bytes) = fs::read(&metadata_path)
            && let Ok(serde_json::Value::Object(recorded)) = serde_json::from_slice(&bytes)
            && let Some(vault) = recorded.get("vault_path").and_then(|value| value.as_str())
            && !vault.is_empty()
        {
            entry.vault_path = vault.to_owned();
            entry.last_used = recorded
                .get("last_used")
                .and_then(|value| value.as_str())
                .and_then(go_time_json)
                .unwrap_or_else(|| ZERO_TIME.to_owned());
            entry.metadata = true;
            entry.orphan = matches!(
                fs::metadata(vault),
                Err(ref error) if error.kind() == std::io::ErrorKind::NotFound
            );
        }
        result.push(entry);
    }
    result.sort_by(|left, right| left.db_path.cmp(&right.db_path));
    Ok(result)
}

fn retrieval_index_path(vault_path: &str, db_path: &Path) -> PathBuf {
    let fallback = db_path
        .parent()
        .unwrap_or_else(|| Path::new("."))
        .join("retrieval.db");
    if vault_path.is_empty() {
        return fallback;
    }
    let (Ok(environment), Ok(cwd)) = (
        symdesk_core::config::environment_snapshot(),
        std::env::current_dir(),
    ) else {
        return fallback;
    };
    symdesk_index::index_location_for_vault(vault_path, &environment, &cwd, &std::env::temp_dir())
        .unwrap_or(fallback)
}

fn is_file(path: &Path) -> bool {
    fs::metadata(path).is_ok_and(|info| info.is_file())
}

fn report() -> Result<Report, String> {
    let entries = list_sidecars()?;
    let mut report = Report {
        sidecars: Vec::with_capacity(entries.len()),
        count: entries.len(),
        total_bytes: 0,
        orphan_count: 0,
        legacy_path: String::new(),
        legacy_present: false,
        legacy_retrieval_index_path: String::new(),
        legacy_retrieval_index_present: false,
    };
    for entry in entries {
        report.total_bytes += entry.size;
        if entry.orphan {
            report.orphan_count += 1;
        }
        let retrieval = retrieval_index_path(&entry.vault_path, &entry.db_path);
        report.sidecars.push(Entry {
            path: entry.db_path.to_string_lossy().into_owned(),
            vault_path: entry.vault_path,
            last_used: entry.last_used,
            size: entry.size,
            orphan: entry.orphan,
            metadata: entry.metadata,
            retrieval_index_present: is_file(&retrieval),
            retrieval_index_path: retrieval.to_string_lossy().into_owned(),
        });
    }
    let root = sidecar_root()?;
    let legacy = root
        .parent()
        .unwrap_or_else(|| Path::new("."))
        .join("sidecar.db");
    report.legacy_present = is_file(&legacy);
    report.legacy_path = legacy.to_string_lossy().into_owned();
    let environment = symdesk_core::config::environment_snapshot()?;
    let cwd = std::env::current_dir().map_err(|error| error.to_string())?;
    if let Ok(path) =
        symdesk_index::index_location_for_vault("", &environment, &cwd, &std::env::temp_dir())
    {
        report.legacy_retrieval_index_present = is_file(&path);
        report.legacy_retrieval_index_path = path.to_string_lossy().into_owned();
    }
    Ok(report)
}

pub fn run(command: &ArgMatches, json: bool) -> ExitCode {
    let Some((name, sub)) = command.subcommand() else {
        return crate::process_exit(crate::CoreExitCode::Ok);
    };
    let result = if name == "list" {
        report().map(|report| {
            if json {
                return write_go_json(&report);
            }
            let mut text = format!(
                "Sidecars: {}\nTotal size: {} bytes\nOrphaned: {}\n",
                report.count, report.total_bytes, report.orphan_count
            );
            if report.legacy_present {
                text.push_str(&format!(
                    "Legacy sidecar: {} (not managed)\n",
                    report.legacy_path
                ));
            }
            if report.legacy_retrieval_index_present {
                text.push_str(&format!(
                    "Legacy retrieval index: {} (not managed)\n",
                    report.legacy_retrieval_index_path
                ));
            }
            for entry in &report.sidecars {
                let state = if !entry.metadata {
                    "unknown (missing metadata)"
                } else if entry.orphan {
                    "orphan"
                } else {
                    "live"
                };
                text.push_str(&format!(
                    "{state}\t{}\t{} bytes\t{}\t{}\n",
                    entry.vault_path, entry.size, entry.path, entry.retrieval_index_path
                ));
            }
            write_stdout(text)
        })
    } else {
        if !sub.get_one::<bool>("confirm").copied().unwrap_or(false) {
            return emit_error(
                "refusing to remove sidecars without --confirm".to_owned(),
                json,
            );
        }
        prune().map(|removed| {
            if json {
                #[derive(Serialize)]
                struct Removed {
                    removed: usize,
                }
                write_go_json(&Removed { removed })
            } else {
                write_stdout(format!("Removed {removed} orphan sidecar(s).\n"))
            }
        })
    };
    result.unwrap_or_else(|error| emit_error(error, json))
}

fn prune() -> Result<usize, String> {
    let mut removed = 0;
    for entry in list_sidecars()? {
        if !entry.orphan || !entry.metadata {
            continue;
        }
        let directory = entry.db_path.parent().expect("sidecar has a directory");
        fs::remove_dir_all(directory).map_err(|error| {
            format!(
                "remove orphan sidecar {}: {}",
                entry.db_path.display(),
                os_error_text(&error)
            )
        })?;
        removed += 1;
    }
    Ok(removed)
}

/// Go's `syscall.Errno` text: lower-case, without Rust's `(os error N)`.
fn os_error_text(error: &std::io::Error) -> String {
    let text = error.to_string();
    let text = text.split(" (os error").next().unwrap_or(&text);
    let mut chars = text.chars();
    chars
        .next()
        .map(|first| first.to_lowercase().collect::<String>() + chars.as_str())
        .unwrap_or_default()
}

#[cfg(test)]
mod tests {
    use super::go_time_json;

    /// `time.Time.MarshalJSON` of a parsed RFC 3339 value (the process
    /// fixture hides wall-clock timestamps, so the layout is pinned here).
    #[test]
    fn last_used_uses_go_time_json_layout() {
        for (raw, want) in [
            ("2024-05-06T07:08:09.5Z", "2024-05-06T07:08:09.5Z"),
            ("2024-05-06T07:08:09.500000000Z", "2024-05-06T07:08:09.5Z"),
            ("2024-05-06T07:08:09.123456789Z", "2024-05-06T07:08:09.123456789Z"),
            ("2024-05-06T07:08:09Z", "2024-05-06T07:08:09Z"),
            ("2024-05-06T09:08:09+02:00", "2024-05-06T09:08:09+02:00"),
            ("2024-05-06T09:08:09-05:30", "2024-05-06T09:08:09-05:30"),
            ("2024-05-06T09:08:09+00:00", "2024-05-06T09:08:09Z"),
        ] {
            assert_eq!(go_time_json(raw).as_deref(), Some(want), "{raw}");
        }
        assert_eq!(go_time_json("not a time"), None);
    }
}
