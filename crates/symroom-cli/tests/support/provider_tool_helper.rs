#![deny(unsafe_code)]

use std::{
    env,
    fs::OpenOptions,
    io::Write,
    path::Path,
    process::ExitCode,
};

/// Fake `symvault` / `security`: logs argv, prints the configured stdout and
/// stderr verbatim and exits with the configured code. Behaviour matches the
/// Go helper the oracle fixture was recorded with.
fn main() -> ExitCode {
    let arguments = env::args().collect::<Vec<_>>();
    let name = Path::new(&arguments[0])
        .file_name()
        .and_then(|name| name.to_str())
        .expect("tool executable name")
        .trim_end_matches(".exe")
        .to_owned();
    if let Ok(log) = env::var("PROVIDER_LOG")
        && !log.is_empty()
        && let Ok(mut file) = OpenOptions::new().append(true).create(true).open(log)
    {
        let _ = writeln!(file, "{name} {}", arguments[1..].join(" "));
    }
    let upper = name.to_uppercase();
    let _ = std::io::stdout().write_all(
        env::var(format!("PROVIDER_STDOUT_{upper}"))
            .unwrap_or_default()
            .as_bytes(),
    );
    let _ = std::io::stderr().write_all(
        env::var(format!("PROVIDER_STDERR_{upper}"))
            .unwrap_or_default()
            .as_bytes(),
    );
    match env::var(format!("PROVIDER_EXIT_{upper}"))
        .ok()
        .and_then(|code| code.parse::<u8>().ok())
    {
        Some(code) => ExitCode::from(code),
        None => ExitCode::SUCCESS,
    }
}
