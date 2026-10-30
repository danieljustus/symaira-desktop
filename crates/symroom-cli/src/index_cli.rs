#![deny(unsafe_code)]

use std::{
    ffi::OsString,
    io::{self, Write},
    path::PathBuf,
    process::ExitCode,
};

use symroom_core::index;

const DB_PATH: &str = ".symroom/index.sqlite";

pub fn run(args: &[OsString]) -> ExitCode {
    if args.first().is_none_or(|arg| arg != "rebuild") {
        return write(io::stdout(), "Usage: symroom index rebuild\n", 0);
    }
    let room = std::env::var_os("SYMROOM_ROOM_DIR")
        .filter(|value| !value.is_empty())
        .map(PathBuf::from)
        .unwrap_or_else(|| PathBuf::from("."));
    match index::rebuild(&PathBuf::from(DB_PATH), &room) {
        Ok(()) => write(io::stdout(), &rebuilt_message(std::path::MAIN_SEPARATOR), 0),
        Err(error) => write(
            io::stderr(),
            &format!("Error rebuilding index: {error}\n"),
            1,
        ),
    }
}

/// Go prints `filepath.Join(".symroom", "index.sqlite")`: the host separator.
fn rebuilt_message(separator: char) -> String {
    format!("Rebuilt derived index at .symroom{separator}index.sqlite\n")
}

fn write(mut output: impl Write, message: &str, code: u8) -> ExitCode {
    if output.write_all(message.as_bytes()).is_err() {
        return ExitCode::from(1);
    }
    ExitCode::from(code)
}

#[cfg(test)]
mod tests {
    use super::rebuilt_message;

    #[test]
    fn success_message_uses_the_host_separator_of_each_family() {
        assert_eq!(
            rebuilt_message('/'),
            "Rebuilt derived index at .symroom/index.sqlite\n"
        );
        assert_eq!(
            rebuilt_message('\\'),
            "Rebuilt derived index at .symroom\\index.sqlite\n"
        );
    }
}
