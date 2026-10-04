#![deny(unsafe_code)]

use std::{env, fs::OpenOptions, io::Write, path::Path};

fn main() {
    let arguments = env::args().collect::<Vec<_>>();
    let Some(command) = arguments.get(1) else {
        return;
    };
    let name = Path::new(&arguments[0])
        .file_name()
        .and_then(|name| name.to_str())
        .expect("tool executable name")
        .trim_end_matches(".exe");
    if let Ok(log) = env::var("DOCTOR_TOOL_LOG")
        && !log.is_empty()
        && let Ok(mut file) = OpenOptions::new().append(true).create(true).open(log)
    {
        let _ = writeln!(file, "{name} {}", arguments[1..].join(" "));
    }
    match command.as_str() {
        "get" => println!("{}", env::var("DOCTOR_IDENTITY_KEY").unwrap_or_default()),
        "version" => println!("{{\"version\":\"{name}-1.2.3\"}}"),
        _ => {}
    }
}
