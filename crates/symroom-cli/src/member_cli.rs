#![deny(unsafe_code)]

use std::{
    collections::BTreeMap,
    ffi::OsString,
    io::{self, Write},
    path::PathBuf,
    process::ExitCode,
};

use serde::Serialize;
use symaira_core_exit::ExitCode as CoreExitCode;
use symroom_core::{identity, members};

const USAGE: &str = "Usage: symroom member <add|list|remove|role> [flags] [args]\n";
const ADD_FLAGS: &str = "Usage of member add:\n  -identity string\n    \tCaller identity name (must be the room owner)\n  -kind string\n    \tMember kind (human|agent) (default \"human\")\n  -name string\n    \tMember display name\n  -pubkey string\n    \tMember public key (hex)\n  -role string\n    \tMember role (owner|member|agent|observer) (default \"member\")\n";
const LIST_FLAGS: &str = "Usage of member list:\n  -json\n    \tOutput members as JSON\n";
const REMOVE_FLAGS: &str = "Usage of member remove:\n  -identity string\n    \tCaller identity name (must be the room owner)\n";
const ROLE_FLAGS: &str = "Usage of member role:\n  -identity string\n    \tCaller identity name (must be the room owner)\n";

#[derive(Default)]
struct Parsed {
    values: BTreeMap<String, String>,
    positionals: Vec<String>,
}

pub fn run(args: &[OsString]) -> ExitCode {
    let Some(action) = args.first() else {
        return stdout(USAGE, CoreExitCode::Ok);
    };
    match action.to_string_lossy().as_ref() {
        "--help" | "-h" => stdout(USAGE, CoreExitCode::Ok),
        "add" => add(&args[1..]),
        "list" => list(&args[1..]),
        "remove" => remove(&args[1..]),
        "role" => role(&args[1..]),
        other => stderr(
            &format!("Unknown member action: {other}\n"),
            CoreExitCode::NoInput,
        ),
    }
}

fn add(args: &[OsString]) -> ExitCode {
    let parsed = match parse_flags(
        args,
        &["identity", "kind", "name", "pubkey", "role"],
        &[],
        ADD_FLAGS,
    ) {
        Ok(parsed) => parsed,
        Err(code) => return code,
    };
    let mut name = parsed.values.get("name").cloned().unwrap_or_default();
    let mut public_key = parsed.values.get("pubkey").cloned().unwrap_or_default();
    if parsed.positionals.len() >= 2 {
        name.clone_from(&parsed.positionals[0]);
        public_key.clone_from(&parsed.positionals[1]);
    }
    if public_key.is_empty() || name.is_empty() {
        return stderr(
            "Usage: symroom member add [--identity <name>] <name> <pubkey> [--role <role>] [--kind <kind>]\n       symroom member add --pubkey <hex> --name <name> [--role <role>] [--kind <kind>] [--identity <name>]\n",
            CoreExitCode::NoInput,
        );
    }
    let identity_name = parsed.values.get("identity").map_or("", String::as_str);
    let signer = match resolve_identity(identity_name) {
        Ok(signer) => signer,
        Err(code) => return code,
    };
    let role_value = parsed.values.get("role").map_or("member", String::as_str);
    let kind_value = parsed.values.get("kind").map_or("human", String::as_str);
    let event = match members::add_member(
        &room_dir(),
        &name,
        &public_key,
        role_value,
        kind_value,
        &signer,
    ) {
        Ok(event) => event,
        Err(error) => return member_error(error),
    };
    let public_bytes = match hex::decode(&public_key) {
        Ok(bytes) => bytes,
        Err(_) => return stderr("Invalid public key hex\n", CoreExitCode::NoInput),
    };
    let member_id = identity::compute_member_id(&public_bytes);
    stdout(
        &format!(
            "Member added: {name} ({member_id}, role: {role_value}, kind: {kind_value}, event: {})\n",
            event.id
        ),
        CoreExitCode::Ok,
    )
}

fn list(args: &[OsString]) -> ExitCode {
    let parsed = match parse_flags(args, &[], &["json"], LIST_FLAGS) {
        Ok(parsed) => parsed,
        Err(code) => return code,
    };
    let json = parsed
        .values
        .get("json")
        .is_some_and(|value| parse_bool(value).unwrap_or(false));
    let state = match members::list_members(&room_dir()) {
        Ok(state) => state,
        Err(error) => {
            return stderr(
                &format!("Error listing members: {error}\n"),
                CoreExitCode::Generic,
            );
        }
    };
    if state.members.is_empty() {
        return stdout(if json { "[]\n" } else { "No members\n" }, CoreExitCode::Ok);
    }
    let entries = state.members.into_values().collect::<Vec<_>>();
    if json {
        let output = entries
            .iter()
            .map(|member| ListMember {
                id: &member.id,
                name: &member.name,
                role: &member.role,
                kind: &member.kind,
            })
            .collect::<Vec<_>>();
        return match go_json(&output) {
            Ok(mut output) => {
                output.push('\n');
                stdout(&output, CoreExitCode::Ok)
            }
            Err(error) => stderr(
                &format!("Error listing members: {error}\n"),
                CoreExitCode::Generic,
            ),
        };
    }
    stdout(&render_table(&entries), CoreExitCode::Ok)
}

#[derive(Serialize)]
struct ListMember<'a> {
    id: &'a str,
    name: &'a str,
    role: &'a str,
    kind: &'a str,
}

fn remove(args: &[OsString]) -> ExitCode {
    let parsed = match parse_flags(args, &["identity"], &[], REMOVE_FLAGS) {
        Ok(parsed) => parsed,
        Err(code) => return code,
    };
    let Some(member_id) = parsed.positionals.first() else {
        return stderr(
            "Usage: symroom member remove [--identity <name>] <member_id>\n",
            CoreExitCode::NoInput,
        );
    };
    let signer = match resolve_identity(parsed.values.get("identity").map_or("", String::as_str)) {
        Ok(signer) => signer,
        Err(code) => return code,
    };
    match members::remove_member(&room_dir(), member_id, &signer) {
        Ok(event) => stdout(
            &format!("Member removed: {member_id} (event: {})\n", event.id),
            CoreExitCode::Ok,
        ),
        Err(error) => member_error(error),
    }
}

fn role(args: &[OsString]) -> ExitCode {
    let parsed = match parse_flags(args, &["identity"], &[], ROLE_FLAGS) {
        Ok(parsed) => parsed,
        Err(code) => return code,
    };
    if parsed.positionals.len() < 2 {
        return stderr(
            "Usage: symroom member role [--identity <name>] <member_id> <role>\n",
            CoreExitCode::NoInput,
        );
    }
    let member_id = &parsed.positionals[0];
    let role_value = &parsed.positionals[1];
    let signer = match resolve_identity(parsed.values.get("identity").map_or("", String::as_str)) {
        Ok(signer) => signer,
        Err(code) => return code,
    };
    match members::set_member_role(&room_dir(), member_id, role_value, &signer) {
        Ok(event) => stdout(
            &format!(
                "Updated role for {member_id} to {role_value} (event: {})\n",
                event.id
            ),
            CoreExitCode::Ok,
        ),
        Err(error) => member_error(error),
    }
}

fn parse_flags(
    args: &[OsString],
    string_flags: &[&str],
    bool_flags: &[&str],
    usage: &str,
) -> Result<Parsed, ExitCode> {
    let mut parsed = Parsed::default();
    let mut parsing_flags = true;
    let mut index = 0;
    while index < args.len() {
        let argument = args[index].to_string_lossy();
        if parsing_flags && argument == "--" {
            parsing_flags = false;
            index += 1;
            continue;
        }
        if parsing_flags && argument.starts_with('-') {
            let flag = argument.trim_start_matches('-');
            if matches!(flag, "h" | "help") {
                return Err(stderr(usage, CoreExitCode::Ok));
            }
            let (name, inline_value) = flag
                .split_once('=')
                .map_or((flag, None), |(name, value)| (name, Some(value)));
            if string_flags.contains(&name) {
                let value = if let Some(value) = inline_value {
                    value.to_owned()
                } else if let Some(value) = args.get(index + 1) {
                    index += 1;
                    value.to_string_lossy().into_owned()
                } else {
                    return Err(stderr(
                        &format!("flag needs an argument: -{name}\n{usage}"),
                        CoreExitCode::NoInput,
                    ));
                };
                parsed.values.insert(name.to_owned(), value);
                index += 1;
                continue;
            }
            if bool_flags.contains(&name) {
                let value = match inline_value {
                    Some(value) => match parse_bool(value) {
                        Some(value) => value,
                        None => {
                            return Err(stderr(
                                &format!(
                                    "invalid boolean value \"{value}\" for -{name}: parse error\n{usage}"
                                ),
                                CoreExitCode::NoInput,
                            ));
                        }
                    },
                    None => true,
                };
                parsed.values.insert(name.to_owned(), value.to_string());
                index += 1;
                continue;
            }
            return Err(stderr(
                &format!("flag provided but not defined: -{name}\n{usage}"),
                CoreExitCode::NoInput,
            ));
        }
        parsing_flags = false;
        parsed.positionals.push(argument.into_owned());
        index += 1;
    }
    Ok(parsed)
}

fn parse_bool(value: &str) -> Option<bool> {
    match value {
        "1" | "t" | "T" | "TRUE" | "True" | "true" => Some(true),
        "0" | "f" | "F" | "FALSE" | "False" | "false" => Some(false),
        _ => None,
    }
}

fn resolve_identity(name: &str) -> Result<identity::Identity, ExitCode> {
    let name = if name.is_empty() {
        match default_identity() {
            Ok(name) if !name.is_empty() => name,
            Ok(_) => {
                return Err(stderr(
                    "Error: --identity is required when default_identity is not configured\n",
                    CoreExitCode::NoInput,
                ));
            }
            Err(error) => {
                return Err(stderr(
                    &format!("Error loading configuration: {error}\n"),
                    CoreExitCode::NoInput,
                ));
            }
        }
    } else {
        name.to_owned()
    };
    identity::load(&name).map_err(|error| {
        stderr(
            &format!("Error loading identity {name}: {error}\n"),
            CoreExitCode::NotFound,
        )
    })
}

pub(crate) fn default_identity() -> Result<String, String> {
    let home = home_dir()?;
    let global_path = home.join(".config/symroom/config.toml");
    let mut name = merge_identity_config(&global_path, "global config error", String::new())?;
    if let Ok(cwd) = std::env::current_dir() {
        name = merge_identity_config(&cwd.join(".symroom.toml"), "project config error", name)?;
    }
    if let Ok(value) = std::env::var("SYMROOM_DEFAULT_IDENTITY")
        && !value.is_empty()
    {
        name = value;
    }
    Ok(name)
}

fn home_dir() -> Result<PathBuf, String> {
    #[cfg(windows)]
    let home = std::env::var_os("USERPROFILE");
    #[cfg(not(windows))]
    let home = std::env::var_os("HOME");
    home.filter(|path| !path.is_empty())
        .map(PathBuf::from)
        .ok_or_else(|| "cannot determine home directory".to_owned())
}

fn merge_identity_config(
    path: &std::path::Path,
    source: &str,
    current: String,
) -> Result<String, String> {
    let contents = match std::fs::read_to_string(path) {
        Ok(contents) => contents,
        Err(error) if error.kind() == io::ErrorKind::NotFound => return Ok(current),
        Err(error) => {
            return Err(format!(
                "{source}: failed to parse {}: {error}",
                path.display()
            ));
        }
    };
    let config: toml::Value = toml::from_str(&contents)
        .map_err(|error| format!("{source}: failed to parse {}: {error}", path.display()))?;
    if config.get("adapters").is_some() {
        return Err(format!(
            "{source}: failed to apply {}: field \"adapters\": map fields are not supported from config",
            path.display()
        ));
    }
    match config.get("default_identity") {
        None => Ok(current),
        Some(toml::Value::String(value)) if value.is_empty() => Ok(current),
        Some(toml::Value::String(value)) => Ok(value.clone()),
        Some(value) => Err(format!(
            "{source}: failed to apply {}: field default_identity: expected string, got {}",
            path.display(),
            match value {
                toml::Value::Integer(_) => "int64",
                toml::Value::Float(_) => "float64",
                toml::Value::Boolean(_) => "bool",
                toml::Value::Datetime(_) => "time.Time",
                toml::Value::Array(_) => "[]interface {}",
                toml::Value::Table(_) => "map[string]interface {}",
                toml::Value::String(_) => unreachable!(),
            }
        )),
    }
}

fn member_error(error: members::MemberMutationError) -> ExitCode {
    match error {
        members::MemberMutationError::Unauthorized => stderr(
            "Error: only room owners can perform member management\n",
            CoreExitCode::Forbidden,
        ),
        members::MemberMutationError::NotFound => {
            stderr("Error: member not found\n", CoreExitCode::NotFound)
        }
        members::MemberMutationError::InvalidRole => stderr(
            "Error: invalid member role (valid: owner|member|agent|observer)\n",
            CoreExitCode::NoInput,
        ),
        members::MemberMutationError::InvalidKind => stderr(
            "Error: invalid member kind (valid: human|agent)\n",
            CoreExitCode::NoInput,
        ),
        error => stderr(&format!("Error: {error}\n"), CoreExitCode::Generic),
    }
}

fn render_table(members: &[members::Member]) -> String {
    let rows = members
        .iter()
        .map(|member| {
            [
                member.id.as_str(),
                member.name.as_str(),
                member.role.as_str(),
                member.kind.as_str(),
            ]
        })
        .collect::<Vec<_>>();
    let headers = ["ID", "NAME", "ROLE", "KIND"];
    let widths = (0..headers.len())
        .map(|index| {
            rows.iter()
                .map(|row| row[index].chars().count())
                .fold(headers[index].len(), usize::max)
        })
        .collect::<Vec<_>>();
    let mut output = format_table_row(&headers, &widths);
    for row in rows {
        output.push_str(&format_table_row(&row, &widths));
    }
    output
}

fn format_table_row(fields: &[&str; 4], widths: &[usize]) -> String {
    let mut row = String::new();
    for (index, field) in fields.iter().enumerate() {
        if index > 0 {
            row.push_str("  ");
        }
        row.push_str(field);
        if index < fields.len() - 1 {
            for _ in field.chars().count()..widths[index] {
                row.push(' ');
            }
        }
    }
    row.push('\n');
    row
}

fn go_json(value: &impl Serialize) -> Result<String, serde_json::Error> {
    Ok(serde_json::to_string_pretty(value)?
        .replace('&', "\\u0026")
        .replace('<', "\\u003c")
        .replace('>', "\\u003e")
        .replace('\u{2028}', "\\u2028")
        .replace('\u{2029}', "\\u2029"))
}

fn room_dir() -> PathBuf {
    std::env::var_os("SYMROOM_ROOM_DIR")
        .filter(|value| !value.is_empty())
        .map(PathBuf::from)
        .unwrap_or_else(|| PathBuf::from("."))
}

fn stdout(value: &str, code: CoreExitCode) -> ExitCode {
    if io::stdout().write_all(value.as_bytes()).is_err() {
        process_exit(CoreExitCode::Generic)
    } else {
        process_exit(code)
    }
}

fn stderr(value: &str, code: CoreExitCode) -> ExitCode {
    if io::stderr().write_all(value.as_bytes()).is_err() {
        process_exit(CoreExitCode::Generic)
    } else {
        process_exit(code)
    }
}

fn process_exit(code: CoreExitCode) -> ExitCode {
    ExitCode::from(code.as_u8())
}
