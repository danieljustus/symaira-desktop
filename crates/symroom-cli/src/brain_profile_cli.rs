#![deny(unsafe_code)]

use std::{
    ffi::OsString,
    fs::{self, OpenOptions},
    io::{self, Write},
    path::{Path, PathBuf},
    process::{Command, ExitCode, Stdio},
};

use symaira_core_exit::ExitCode as CoreExitCode;
use symroom_core::{journal, members};

const FLAGS_USAGE: &str = "Usage of brain-profile:\n  -install\n    \tInstall profile to symbrain config path\n  -member string\n    \tMember ID for the agent\n";

#[derive(Default)]
struct Parsed {
    member: String,
    install: bool,
}

pub fn run(args: &[OsString]) -> ExitCode {
    let parsed = match parse(args) {
        Ok(parsed) => parsed,
        Err(code) => return code,
    };
    if parsed.member.is_empty() {
        return stderr(
            "Usage: symroom brain-profile --member <id> [--install]\n",
            CoreExitCode::NoInput,
        );
    }

    let room = room_dir();
    let events = match journal::merge_all(&room) {
        Ok(events) => events,
        Err(error) => {
            return stderr(
                &format!("Error generating brain profile: {error}\n"),
                CoreExitCode::Generic,
            );
        }
    };
    let mut state = members::State::default();
    for event in &events {
        if let Err(error) = state.apply_event(event) {
            return stderr(
                &format!("Error generating brain profile: {error}\n"),
                CoreExitCode::Generic,
            );
        }
    }
    let Some(member) = state.members.get(&parsed.member) else {
        return stderr(
            &format!(
                "Error generating brain profile: member {} not found in room\n",
                parsed.member
            ),
            CoreExitCode::Generic,
        );
    };

    let room_name = events
        .iter()
        .find(|event| event.kind == "room.created")
        .and_then(|event| serde_json::from_str::<serde_json::Value>(event.body.get()).ok())
        .and_then(|body| {
            body.get("name")
                .and_then(serde_json::Value::as_str)
                .map(str::to_owned)
        })
        .filter(|name| !name.is_empty())
        .unwrap_or_else(|| "symaira-room".to_owned());
    let slug = room_name.to_lowercase().replace(' ', "-");
    let name = format!("room-{slug}");
    let content = format!(
        "[profile]\nname = \"{name}\"\nmember_id = \"{}\"\nrole = \"{}\"\n\n[permissions]\napprove_runs = false\nresolve_checkpoints = false\nread_journal = true\nwrite_notes = true\n",
        member.id, member.role
    );

    if parsed.install {
        match install(&name, content.as_bytes()) {
            Ok(message) => stdout(&format!("{message}\n"), CoreExitCode::Ok),
            Err(error) => stderr(
                &format!("Error installing profile: {error}\n"),
                CoreExitCode::Generic,
            ),
        }
    } else {
        stdout(
            &format!(
                "{content}\n# To install run:\n# symbrain install --harness <harness> --profile {name}\n"
            ),
            CoreExitCode::Ok,
        )
    }
}

fn parse(args: &[OsString]) -> Result<Parsed, ExitCode> {
    let mut parsed = Parsed::default();
    let mut index = 0;
    while index < args.len() {
        let argument = args[index].to_string_lossy();
        if !argument.starts_with('-') {
            break;
        }
        if matches!(argument.as_ref(), "-h" | "--help") {
            return Err(stderr(FLAGS_USAGE, CoreExitCode::Ok));
        }
        let flag = argument.trim_start_matches('-');
        let (name, inline) = flag
            .split_once('=')
            .map_or((flag, None), |(name, value)| (name, Some(value)));
        match name {
            "install" => {
                parsed.install = match inline {
                    Some("true" | "TRUE" | "True" | "T" | "t" | "1") => true,
                    Some("false" | "FALSE" | "False" | "F" | "f" | "0") => false,
                    Some(value) => {
                        return Err(stderr(
                            &format!(
                                "invalid boolean value \"{value}\" for -install: parse error\n{FLAGS_USAGE}"
                            ),
                            CoreExitCode::NoInput,
                        ));
                    }
                    None => true,
                };
            }
            "member" => {
                if let Some(value) = inline {
                    parsed.member = value.to_owned();
                } else if let Some(value) = args.get(index + 1) {
                    index += 1;
                    parsed.member = value.to_string_lossy().into_owned();
                } else {
                    return Err(stderr(
                        &format!("flag needs an argument: -member\n{FLAGS_USAGE}"),
                        CoreExitCode::NoInput,
                    ));
                }
            }
            _ => {
                return Err(stderr(
                    &format!("flag provided but not defined: -{name}\n{FLAGS_USAGE}"),
                    CoreExitCode::NoInput,
                ));
            }
        }
        index += 1;
    }
    Ok(parsed)
}

fn install(name: &str, content: &[u8]) -> Result<String, String> {
    let command = Command::new("symbrain")
        .args(["profile", "add", name])
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn();
    match command {
        Ok(mut child) => {
            if let Some(mut stdin) = child.stdin.take() {
                stdin
                    .write_all(content)
                    .map_err(|error| error.to_string())?;
            }
            let output = child
                .wait_with_output()
                .map_err(|error| error.to_string())?;
            if output.status.success() {
                let mut combined = output.stdout;
                combined.extend_from_slice(&output.stderr);
                return String::from_utf8(combined).map_err(|error| error.to_string());
            }
            write_fallback(name, content, false)
        }
        Err(error) if error.kind() == io::ErrorKind::NotFound => {
            write_fallback(name, content, true)
        }
        Err(error) => write_fallback(name, content, false)
            .map_err(|write_error| format!("{error}; fallback failed: {write_error}")),
    }
}

fn write_fallback(name: &str, content: &[u8], absent: bool) -> Result<String, String> {
    let home = home_dir().ok_or_else(|| {
        if absent {
            "symbrain not installed and user home not found: unable to resolve home directory"
                .to_owned()
        } else {
            "resolve user home for profile fallback: unable to resolve home directory".to_owned()
        }
    })?;
    let dir = home.join(".config/symbrain/profiles");
    create_dirs_0700(&dir).map_err(|error| {
        if absent {
            error.to_string()
        } else {
            format!("create profile directory: {error}")
        }
    })?;
    let path = dir.join(format!("{name}.toml"));
    let file = open_profile(&path, content).map_err(|error| {
        if absent {
            error.to_string()
        } else {
            format!("write profile fallback: {error}")
        }
    })?;
    drop(file);
    if absent {
        Ok(format!(
            "symbrain is not installed on PATH. Profile written to {}",
            path.display()
        ))
    } else {
        Ok(format!("Profile written to {}", path.display()))
    }
}

fn create_dirs_0700(path: &Path) -> io::Result<()> {
    let mut current = PathBuf::new();
    for component in path.components() {
        current.push(component);
        match fs::create_dir(&current) {
            Ok(()) => set_mode(&current, 0o700)?,
            Err(error) if error.kind() == io::ErrorKind::AlreadyExists => {}
            Err(error) => return Err(error),
        }
    }
    Ok(())
}

fn open_profile(path: &Path, content: &[u8]) -> io::Result<fs::File> {
    let mut options = OpenOptions::new();
    options.write(true).create(true).truncate(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        options.mode(0o600);
    }
    let mut file = options.open(path)?;
    file.write_all(content)?;
    Ok(file)
}

fn set_mode(path: &Path, mode: u32) -> io::Result<()> {
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        fs::set_permissions(path, fs::Permissions::from_mode(mode))
    }
    #[cfg(not(unix))]
    {
        let _ = (path, mode);
        Ok(())
    }
}

fn home_dir() -> Option<PathBuf> {
    #[cfg(windows)]
    if let Some(home) = std::env::var_os("USERPROFILE").filter(|value| !value.is_empty()) {
        return Some(PathBuf::from(home));
    }
    std::env::var_os("HOME")
        .filter(|value| !value.is_empty())
        .map(PathBuf::from)
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
