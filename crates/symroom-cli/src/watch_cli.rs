#![deny(unsafe_code)]

use std::{
    ffi::OsString,
    io::{self, BufReader, Write},
    path::{Path, PathBuf},
    process::{Command, ExitCode, Stdio},
    sync::{
        Arc,
        atomic::{AtomicBool, Ordering},
    },
    thread,
    time::Duration,
};

use crate::member_cli;
use symaira_core_exit::ExitCode as CoreExitCode;
use symroom_core::{artifact, desk_watch, identity};

const USAGE: &str = "Usage: symroom watch --desk <vault> [--identity <name>]\n";
const FLAGS_USAGE: &str = "Usage of watch:\n  -desk string\n    \tSymdesk vault name to watch\n  -identity string\n    \tAuthor identity name\n";

#[derive(Default)]
struct Parsed {
    desk: String,
    identity: String,
}

pub fn run(args: &[OsString]) -> ExitCode {
    let parsed = match parse(args) {
        Ok(parsed) => parsed,
        Err(code) => return code,
    };
    if parsed.desk.is_empty() {
        return stdout(USAGE, CoreExitCode::Ok);
    }
    let identity_name = if parsed.identity.is_empty() {
        match member_cli::default_identity() {
            Ok(name) => name,
            Err(error) => {
                return stderr(
                    &format!("Error loading configuration: {error}\n"),
                    CoreExitCode::NoInput,
                );
            }
        }
    } else {
        parsed.identity
    };
    if identity_name.is_empty() {
        return stderr(
            "Error: --identity is required when default_identity is not configured\n",
            CoreExitCode::NoInput,
        );
    }
    let signer = match identity::load(&identity_name) {
        Ok(identity) => identity,
        Err(error) => {
            return stderr(
                &format!("Error loading identity {identity_name}: {error}\n"),
                CoreExitCode::NotFound,
            );
        }
    };

    let runtime = match tokio::runtime::Builder::new_current_thread()
        .enable_all()
        .build()
    {
        Ok(runtime) => runtime,
        Err(error) => {
            return stderr(&format!("Watch error: {error}\n"), CoreExitCode::Generic);
        }
    };
    runtime.block_on(watch(&parsed.desk, signer))
}

async fn watch(vault: &str, signer: identity::Identity) -> ExitCode {
    #[cfg(unix)]
    let mut terminate =
        match tokio::signal::unix::signal(tokio::signal::unix::SignalKind::terminate()) {
            Ok(signal) => signal,
            Err(error) => {
                return stderr(&format!("Watch error: {error}\n"), CoreExitCode::Generic);
            }
        };
    let room = room_dir();
    if stdout(
        &format!("Watching symdesk vault {vault}...\n"),
        CoreExitCode::Ok,
    ) != process_exit(CoreExitCode::Ok)
    {
        return process_exit(CoreExitCode::Generic);
    }

    let mut backoff = Duration::from_millis(100);
    loop {
        let mut child = match Command::new("symdesk")
            .args(["events", vault])
            .stdout(Stdio::piped())
            .stderr(Stdio::null())
            .spawn()
        {
            Ok(child) => child,
            Err(error) if error.kind() == io::ErrorKind::NotFound => {
                return stderr(
                    "Watch error: symdesk binary not found\n",
                    CoreExitCode::Generic,
                );
            }
            Err(error) => {
                let _ = stderr(
                    &format!(
                        "symdesk events start error: {error}, retrying in {}...\n",
                        go_duration(backoff)
                    ),
                    CoreExitCode::Ok,
                );
                if interrupted_or_timeout(
                    backoff,
                    #[cfg(unix)]
                    &mut terminate,
                )
                .await
                {
                    return process_exit(CoreExitCode::Ok);
                }
                backoff = (backoff * 2).min(Duration::from_secs(2));
                continue;
            }
        };

        let Some(output) = child.stdout.take() else {
            return stderr(
                "Watch error: symdesk stdout unavailable\n",
                CoreExitCode::Generic,
            );
        };
        let cancelled = Arc::new(AtomicBool::new(false));
        let reader_cancelled = Arc::clone(&cancelled);
        let reader_room = room.clone();
        let reader_signer = signer.clone();
        let reader = thread::spawn(move || {
            let mut output = BufReader::new(output);
            let _ = desk_watch::watch_stream(
                &mut output,
                || reader_cancelled.load(Ordering::Acquire),
                |item| {
                    artifact::handle_desk_event(&reader_room, Path::new(""), item, &reader_signer)
                        .map_err(|error| error.to_string())
                },
            );
        });

        loop {
            if child.try_wait().ok().flatten().is_some() {
                break;
            }
            if interrupted_or_timeout(
                Duration::from_millis(20),
                #[cfg(unix)]
                &mut terminate,
            )
            .await
            {
                cancelled.store(true, Ordering::Release);
                let _ = child.kill();
                let _ = child.wait();
                let _ = reader.join();
                return process_exit(CoreExitCode::Ok);
            }
        }
        let _ = child.wait();
        let _ = reader.join();
        let _ = stderr(
            &format!(
                "symdesk events exited, retrying in {}...\n",
                go_duration(backoff)
            ),
            CoreExitCode::Ok,
        );
        if interrupted_or_timeout(
            backoff,
            #[cfg(unix)]
            &mut terminate,
        )
        .await
        {
            return process_exit(CoreExitCode::Ok);
        }
        backoff = (backoff * 2).min(Duration::from_secs(2));
    }
}

async fn interrupted_or_timeout(
    duration: Duration,
    #[cfg(unix)] terminate: &mut tokio::signal::unix::Signal,
) -> bool {
    #[cfg(unix)]
    tokio::select! {
        _ = tokio::signal::ctrl_c() => true,
        _ = terminate.recv() => true,
        _ = tokio::time::sleep(duration) => false,
    }
    #[cfg(not(unix))]
    tokio::select! {
        _ = tokio::signal::ctrl_c() => true,
        _ = tokio::time::sleep(duration) => false,
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
        let target = match name {
            "desk" => &mut parsed.desk,
            "identity" => &mut parsed.identity,
            _ => {
                return Err(stderr(
                    &format!("flag provided but not defined: -{name}\n{FLAGS_USAGE}"),
                    CoreExitCode::NoInput,
                ));
            }
        };
        if let Some(value) = inline {
            *target = value.to_owned();
        } else if let Some(value) = args.get(index + 1) {
            index += 1;
            *target = value.to_string_lossy().into_owned();
        } else {
            return Err(stderr(
                &format!("flag needs an argument: -{name}\n{FLAGS_USAGE}"),
                CoreExitCode::NoInput,
            ));
        }
        index += 1;
    }
    Ok(parsed)
}

fn room_dir() -> PathBuf {
    std::env::var_os("SYMROOM_ROOM_DIR")
        .filter(|value| !value.is_empty())
        .map(PathBuf::from)
        .unwrap_or_else(|| PathBuf::from("."))
}

fn go_duration(duration: Duration) -> String {
    if duration.as_secs() > 0 {
        format!("{}s", duration.as_secs())
    } else {
        format!("{}ms", duration.as_millis())
    }
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
