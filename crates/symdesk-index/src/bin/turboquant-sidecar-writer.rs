//! Writes Rust-encoded TurboQuant sidecars for the Go read-back gate (#1137).
//!
//! Usage: `turboquant-sidecar-writer <property-corpus.json> <output.json>`.

use std::{env, fs, process::ExitCode};

use symdesk_index::turboquant::sidecar_corpus::render_sidecar_corpus;

fn main() -> ExitCode {
    let args: Vec<String> = env::args().collect();
    let [_, input, output] = args.as_slice() else {
        eprintln!("usage: turboquant-sidecar-writer <property-corpus.json> <output.json>");
        return ExitCode::from(2);
    };
    let property = match fs::read_to_string(input) {
        Ok(text) => text,
        Err(error) => {
            eprintln!("read {input}: {error}");
            return ExitCode::FAILURE;
        }
    };
    let rendered = match render_sidecar_corpus(&property) {
        Ok(text) => text,
        Err(error) => {
            eprintln!("render sidecar corpus: {error}");
            return ExitCode::FAILURE;
        }
    };
    if let Err(error) = fs::write(output, rendered) {
        eprintln!("write {output}: {error}");
        return ExitCode::FAILURE;
    }
    ExitCode::SUCCESS
}
