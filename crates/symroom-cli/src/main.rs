#![deny(unsafe_code)]

fn main() -> std::process::ExitCode {
    symroom_cli::run(std::env::args_os())
}
