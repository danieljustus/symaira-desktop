#![deny(unsafe_code)]

use std::{
    collections::BTreeMap,
    ffi::OsString,
    fs,
    io::{self, Write},
    path::{Path, PathBuf},
    process::{Command, ExitCode},
};

use serde::Serialize;
use symaira_core_exit::ExitCode as CoreExitCode;
use symroom_core::{identity, journal};

const FLAG_USAGE: &str = "Usage of doctor:\n  -json\n    \tEmit stable machine-readable JSON\n";

#[derive(Clone, Copy, Serialize)]
#[serde(rename_all = "lowercase")]
enum Status {
    Ok,
    Warn,
    Fail,
}

impl Status {
    fn upper(self) -> &'static str {
        match self {
            Self::Ok => "OK",
            Self::Warn => "WARN",
            Self::Fail => "FAIL",
        }
    }
}

#[derive(Serialize)]
struct Check {
    name: String,
    status: Status,
    message: String,
    remediation: String,
}

#[derive(Serialize)]
struct Tool {
    name: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    path: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    version: Option<String>,
    status: Status,
    remediation: String,
}

#[derive(Serialize)]
struct Report {
    checks: Vec<Check>,
    tools: Vec<Tool>,
    failed: bool,
}

impl Report {
    fn add(&mut self, name: &str, status: Status, message: impl Into<String>, remediation: &str) {
        self.failed |= matches!(status, Status::Fail);
        self.checks.push(Check {
            name: name.to_owned(),
            status,
            message: message.into(),
            remediation: remediation.to_owned(),
        });
    }
}

pub fn run(args: &[OsString]) -> ExitCode {
    let json = match parse_flags(args) {
        Ok(json) => json,
        Err(code) => return code,
    };
    let room = room_dir();
    let report = run_doctor(&room);
    if json {
        let mut output = match serde_json::to_string_pretty(&report) {
            Ok(output) => output
                .replace('&', "\\u0026")
                .replace('<', "\\u003c")
                .replace('>', "\\u003e")
                .replace('\u{2028}', "\\u2028")
                .replace('\u{2029}', "\\u2029"),
            Err(_) => return process_exit(CoreExitCode::Generic),
        };
        output.push('\n');
        stdout(
            &output,
            if report.failed {
                CoreExitCode::Generic
            } else {
                CoreExitCode::Ok
            },
        )
    } else {
        let mut output = String::new();
        for check in &report.checks {
            output.push_str(&format!(
                "[{}] {}: {}\n  remediation: {}\n",
                check.status.upper(),
                check.name,
                check.message,
                check.remediation
            ));
        }
        for tool in &report.tools {
            output.push_str(&format!(
                "[{}] {}: {}",
                tool.status.upper(),
                tool.name,
                tool.path.as_deref().unwrap_or("")
            ));
            if let Some(version) = &tool.version {
                output.push_str(&format!(" ({version})"));
            }
            output.push_str(&format!("\n  remediation: {}\n", tool.remediation));
        }
        stdout(
            &output,
            if report.failed {
                CoreExitCode::Generic
            } else {
                CoreExitCode::Ok
            },
        )
    }
}

fn run_doctor(room: &Path) -> Report {
    let mut report = Report {
        checks: Vec::new(),
        tools: Vec::new(),
        failed: false,
    };
    if room.join("room.toml").exists() {
        report.add(
            "room_manifest",
            Status::Ok,
            "room.toml is present",
            "No action needed.",
        );
    } else {
        report.add(
            "room_manifest",
            Status::Fail,
            "room.toml is missing",
            "Run `symroom init <directory> --identity <name>` in an empty room directory.",
        );
    }
    let dot = room.join(".symroom");
    if dot.exists() {
        report.add(
            "sync_folder",
            Status::Ok,
            ".symroom exists and is local room state",
            "Keep .symroom out of version control and sync it only through a trusted private folder.",
        );
    } else {
        report.add(
            "sync_folder",
            Status::Warn,
            ".symroom is missing; this may be a non-room directory",
            "Run `symroom init` here, or run doctor from the room root.",
        );
    }

    let (identity_name, config_error) = default_identity();
    if let Some(error) = config_error {
        report.add(
            "identity_config",
            Status::Warn,
            format!("configuration could not be loaded: {error}"),
            "Fix the symroom configuration file or set SYMROOM_DEFAULT_IDENTITY.",
        );
    }
    if identity_name.is_empty() {
        report.add(
            "identity_presence",
            Status::Fail,
            "no default identity is configured",
            "Set `default_identity` in symroom config or create/select an identity with `--identity <name>`.",
        );
    } else {
        match load_doctor_identity(&identity_name) {
            Ok(id) => report.add(
                "identity_resolution",
                Status::Ok,
                format!("identity {identity_name:?} resolves to {}", id.member_id),
                "No action needed.",
            ),
            Err(error) => report.add(
                "identity_resolution",
                Status::Fail,
                format!("identity {identity_name:?} could not be resolved: {error}"),
                "Create it with `symroom identity create <name>` or fix the configured identity name.",
            ),
        }
        let path = identity::identities_dir().join(format!("{identity_name}.json"));
        match fs::metadata(&path) {
            Ok(metadata) => {
                let mode = permissions_mode(&metadata);
                if mode == 0o600 {
                    report.add(
                        "identity_key_mode",
                        Status::Ok,
                        "identity key file mode is 0600",
                        "No action needed.",
                    );
                } else {
                    report.add(
                        "identity_key_mode",
                        Status::Fail,
                        format!("identity key file mode is {mode:04o}"),
                        &format!(
                            "Run `chmod 600 {}` and ensure the identities directory is private.",
                            path.display()
                        )
                    );
                }
            }
            Err(error) if error.kind() == io::ErrorKind::NotFound => report.add(
                "identity_key_mode",
                Status::Warn,
                "identity is supplied by a non-file provider",
                "Confirm the external provider protects the private key and does not expose it in logs.",
            ),
            Err(error) => report.add(
                "identity_key_mode",
                Status::Fail,
                format!("cannot inspect identity key file: {error}"),
                "Restore access to the identity file and verify its permissions are 0600.",
            ),
        }
    }

    duplicate_identity_check(&mut report);
    index_check(&mut report, room, &dot);
    integrity_check(&mut report, room);
    for name in ["symdesk", "symbrain", "symvault"] {
        report.tools.push(check_tool(name));
    }
    report
}

fn default_identity() -> (String, Option<String>) {
    let Some(home) = home_dir() else {
        return (
            String::new(),
            Some("cannot determine home directory".to_owned()),
        );
    };
    let mut name = String::new();
    let global = home.join(".config/symroom/config.toml");
    if let Err(error) = merge_config(&global, "global", &mut name) {
        return (String::new(), Some(error));
    }
    if let Ok(cwd) = std::env::current_dir() {
        let project = cwd.join(".symroom.toml");
        if let Err(error) = merge_config(&project, "project", &mut name) {
            return (String::new(), Some(error));
        }
    }
    if let Ok(value) = std::env::var("SYMROOM_DEFAULT_IDENTITY")
        && !value.is_empty()
    {
        name = value;
    }
    (name, None)
}

fn merge_config(path: &Path, source: &str, name: &mut String) -> Result<(), String> {
    let content = match fs::read_to_string(path) {
        Ok(content) => content,
        Err(error) if error.kind() == io::ErrorKind::NotFound => return Ok(()),
        Err(error) => {
            return Err(format!(
                "{source} config error: failed to parse {}: {error}",
                path.display()
            ));
        }
    };
    let value: toml::Value = toml::from_str(&content).map_err(|error| {
        format!(
            "{source} config error: failed to parse {}: {error}",
            path.display()
        )
    })?;
    let table = value.as_table().expect("TOML root is a table");
    if let Some(default) = table.get("default_identity")
        && !toml_zero(default)
    {
        match default {
            toml::Value::String(value) => name.clone_from(value),
            value => {
                return Err(format!(
                    "{source} config error: failed to apply {}: field \"default_identity\": cannot convert {} to string",
                    path.display(),
                    toml_type(value)
                ));
            }
        }
    }
    if let Some(approval) = table.get("approval")
        && let Some(ttl) = approval
            .as_table()
            .and_then(|table| table.get("default_ttl"))
        && !toml_zero(ttl)
        && !matches!(ttl, toml::Value::String(_))
    {
        return Err(format!(
            "{source} config error: failed to apply {}: field \"approval\": field \"default_ttl\": cannot convert {} to string",
            path.display(),
            toml_type(ttl)
        ));
    }
    if let Some(adapters) = table.get("adapters")
        && !toml_zero(adapters)
    {
        return Err(format!(
            "{source} config error: failed to apply {}: field \"adapters\": map fields are not supported from config",
            path.display()
        ));
    }
    if let Some(enabled) = table
        .get("updatecheck")
        .and_then(|update| update.as_table().and_then(|table| table.get("enabled")))
        && !toml_zero(enabled)
        && !matches!(enabled, toml::Value::Boolean(_))
    {
        return Err(format!(
            "{source} config error: failed to apply {}: field \"updatecheck\": field \"enabled\": cannot convert {} to bool",
            path.display(),
            toml_type(enabled)
        ));
    }
    Ok(())
}

fn toml_zero(value: &toml::Value) -> bool {
    match value {
        toml::Value::String(value) => value.is_empty(),
        toml::Value::Integer(value) => *value == 0,
        toml::Value::Float(value) => *value == 0.0,
        toml::Value::Boolean(value) => !value,
        _ => false,
    }
}

fn toml_type(value: &toml::Value) -> &'static str {
    match value {
        toml::Value::String(_) => "string",
        toml::Value::Integer(_) => "int64",
        toml::Value::Float(_) => "float64",
        toml::Value::Boolean(_) => "bool",
        toml::Value::Datetime(_) => "time.Time",
        toml::Value::Array(_) => "[]interface {}",
        toml::Value::Table(_) => "map[string]interface {}",
    }
}

fn duplicate_identity_check(report: &mut Report) {
    let mut identities = BTreeMap::<String, Vec<String>>::new();
    if let Ok(names) = identity::list() {
        for name in names {
            if let Ok(id) = load_doctor_identity(&name) {
                identities.entry(id.member_id).or_default().push(name);
            }
        }
    }
    let mut duplicates = identities
        .into_iter()
        .filter_map(|(member, names)| {
            (names.len() > 1).then(|| format!("{member} ({})", names.join(", ")))
        })
        .collect::<Vec<_>>();
    duplicates.sort();
    if duplicates.is_empty() {
        report.add(
            "duplicate_identity",
            Status::Ok,
            "no duplicate stored member IDs detected",
            "No action needed.",
        );
    } else {
        report.add(
            "duplicate_identity",
            Status::Fail,
            format!("multiple identity files resolve to the same member ID: {}", duplicates.join("; ")),
            "Remove or rename copied identity files; each device/person should use a deliberate, unique identity.",
        );
    }
}

fn load_doctor_identity(name: &str) -> Result<identity::Identity, identity::IdentityError> {
    if let Ok(raw) = std::env::var("SYMROOM_IDENTITY_KEY")
        && let Ok(bytes) = hex::decode(raw.trim())
        && let Some(identity) = identity::identity_from_private_key(name, &bytes)
    {
        return Ok(identity);
    }

    if let Some(path) = look_path("symvault")
        && let Ok(output) = Command::new(path)
            .args(["get", &format!("symroom/identities/{name}")])
            .output()
        && output.status.success()
        && let Some(identity) = identity::identity_from_private_key(
            name,
            &hex::decode(String::from_utf8_lossy(&output.stdout).trim()).unwrap_or_default(),
        )
    {
        return Ok(identity);
    }

    identity::load(name)
}

fn index_check(report: &mut Report, room: &Path, dot: &Path) {
    let index = dot.join("index.sqlite");
    let metadata = match fs::metadata(&index) {
        Ok(metadata) => metadata,
        Err(_) => {
            report.add(
                "index",
                Status::Warn,
                "derived index is missing",
                "Run `symroom index` to rebuild .symroom/index.sqlite.",
            );
            return;
        }
    };
    let index_time = metadata.modified().ok();
    let stale = newer_in_tree(&room.join("journal"), index_time);
    if stale {
        report.add(
            "index",
            Status::Warn,
            "derived index is older than journal data",
            "Run `symroom index` to rebuild the derived index.",
        );
    } else {
        report.add(
            "index",
            Status::Ok,
            "derived index is present and current",
            "No action needed.",
        );
    }
}

fn newer_in_tree(root: &Path, index_time: Option<std::time::SystemTime>) -> bool {
    let Some(index_time) = index_time else {
        return false;
    };
    let Ok(entries) = fs::read_dir(root) else {
        return false;
    };
    if fs::metadata(root)
        .and_then(|metadata| metadata.modified())
        .is_ok_and(|modified| modified > index_time)
    {
        return true;
    }
    entries.filter_map(Result::ok).any(|entry| {
        if entry
            .metadata()
            .and_then(|metadata| metadata.modified())
            .is_ok_and(|modified| modified > index_time)
        {
            return true;
        }
        entry.file_type().is_ok_and(|kind| kind.is_dir())
            && newer_in_tree(&entry.path(), Some(index_time))
    })
}

fn integrity_check(report: &mut Report, room: &Path) {
    match journal::verify(room) {
        Err(error) => report.add(
            "room_integrity",
            Status::Fail,
            format!("journal verification could not run: read all segments: {error}"),
            "Restore the journal directory and run `symroom verify` for detailed errors.",
        ),
        Ok(result) if !result.valid => report.add(
            "room_integrity",
            Status::Fail,
            format!("journal verification found {} finding(s)", result.findings.len()),
            "Run `symroom verify` and repair or restore the affected journal events from a trusted copy.",
        ),
        Ok(_) => report.add(
            "room_integrity",
            Status::Ok,
            "journal chains and signatures verify",
            "No action needed.",
        ),
    }
}

fn check_tool(name: &str) -> Tool {
    let mut tool = Tool {
        name: name.to_owned(),
        path: None,
        version: None,
        status: Status::Warn,
        remediation: format!(
            "Install {name} and place it on PATH if this integration is needed; otherwise this warning is informational."
        ),
    };
    let Some(path) = look_path(name) else {
        return tool;
    };
    tool.path = Some(path.display().to_string());
    if let Ok(output) = Command::new(&path).args(["version", "--json"]).output() {
        if let Ok(value) = serde_json::from_slice::<serde_json::Value>(&output.stdout)
            && let Some(version) = value.get("version").and_then(serde_json::Value::as_str)
        {
            tool.version = Some(version.to_owned());
        }
        if tool.version.as_deref().unwrap_or_default().is_empty() {
            let output = String::from_utf8_lossy(&output.stdout).trim().to_owned();
            if !output.is_empty() {
                tool.version = Some(output);
            }
        }
    }
    if tool.version.is_some() {
        tool.status = Status::Ok;
        tool.remediation = "No action needed.".to_owned();
    } else {
        tool.remediation =
            format!("Run `{name} version --json` successfully or verify the installed binary.");
    }
    tool
}

fn look_path(name: &str) -> Option<PathBuf> {
    for directory in std::env::split_paths(&std::env::var_os("PATH")?) {
        #[cfg(windows)]
        let names = windows_tool_names(name);
        #[cfg(not(windows))]
        let names = vec![name.to_owned()];
        for candidate in names {
            let path = directory.join(candidate);
            if path.is_file() && is_executable(&path) {
                return Some(path);
            }
        }
    }
    None
}

#[cfg(windows)]
fn windows_tool_names(name: &str) -> Vec<String> {
    if Path::new(name).extension().is_some() {
        return vec![name.to_owned()];
    }
    let extensions = std::env::var_os("PATHEXT")
        .map(|value| {
            value
                .to_string_lossy()
                .split(';')
                .map(|extension| extension.to_ascii_lowercase())
                .filter(|extension| !extension.is_empty())
                .collect::<Vec<_>>()
        })
        .filter(|extensions| !extensions.is_empty())
        .unwrap_or_else(|| vec![".com".to_owned(), ".exe".to_owned()]);
    extensions
        .into_iter()
        .map(|extension| format!("{name}{extension}"))
        .collect()
}

#[cfg(unix)]
fn is_executable(path: &Path) -> bool {
    use std::os::unix::fs::PermissionsExt;
    fs::metadata(path).is_ok_and(|metadata| metadata.permissions().mode() & 0o111 != 0)
}

#[cfg(not(unix))]
fn is_executable(path: &Path) -> bool {
    path.is_file()
}

fn permissions_mode(metadata: &fs::Metadata) -> u32 {
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        metadata.permissions().mode() & 0o777
    }
    #[cfg(not(unix))]
    {
        #[cfg(windows)]
        {
            if metadata.permissions().readonly() {
                0o444
            } else {
                0o666
            }
        }
        #[cfg(not(windows))]
        {
            let _ = metadata;
            0
        }
    }
}

fn parse_flags(args: &[OsString]) -> Result<bool, ExitCode> {
    let mut json = false;
    for argument in args {
        let argument = argument.to_string_lossy();
        if !argument.starts_with('-') {
            break;
        }
        if matches!(argument.as_ref(), "-h" | "--help") {
            return Err(stderr(FLAG_USAGE, CoreExitCode::Ok));
        }
        let flag = argument.trim_start_matches('-');
        let (name, inline) = flag
            .split_once('=')
            .map_or((flag, None), |(name, value)| (name, Some(value)));
        if name != "json" {
            return Err(stderr(
                &format!("flag provided but not defined: -{name}\n{FLAG_USAGE}"),
                CoreExitCode::NoInput,
            ));
        }
        if let Some(value) = inline {
            json = match parse_bool(value) {
                Some(value) => value,
                None => {
                    return Err(stderr(
                        &format!(
                            "invalid boolean value \"{value}\" for -json: parse error\n{FLAG_USAGE}"
                        ),
                        CoreExitCode::NoInput,
                    ));
                }
            };
        } else {
            json = true;
        }
    }
    Ok(json)
}

fn parse_bool(value: &str) -> Option<bool> {
    match value {
        "1" | "t" | "T" | "TRUE" | "True" | "true" => Some(true),
        "0" | "f" | "F" | "FALSE" | "False" | "false" => Some(false),
        _ => None,
    }
}

fn home_dir() -> Option<PathBuf> {
    #[cfg(windows)]
    let home = std::env::var_os("USERPROFILE");
    #[cfg(not(windows))]
    let home = std::env::var_os("HOME");
    home.filter(|value| !value.is_empty()).map(PathBuf::from)
}

fn room_dir() -> PathBuf {
    std::env::var_os("SYMROOM_ROOM_DIR")
        .filter(|value| !value.is_empty())
        .map(PathBuf::from)
        .unwrap_or_else(|| PathBuf::from("."))
}

fn stdout(value: &str, code: CoreExitCode) -> ExitCode {
    write(io::stdout(), value, code)
}

fn stderr(value: &str, code: CoreExitCode) -> ExitCode {
    write(io::stderr(), value, code)
}

fn write(mut output: impl Write, value: &str, code: CoreExitCode) -> ExitCode {
    if output.write_all(value.as_bytes()).is_err() {
        process_exit(CoreExitCode::Generic)
    } else {
        process_exit(code)
    }
}

fn process_exit(code: CoreExitCode) -> ExitCode {
    ExitCode::from(code.as_u8())
}
