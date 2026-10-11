//! Controlled external-tool stand-in; never used by production.
use std::{env, fs, io::Write, path::Path, process, thread, time::Duration};
fn main() {
    let argv: Vec<String> = env::args().collect();
    let tool = Path::new(&argv[0]).file_stem().unwrap().to_str().unwrap();
    let args = &argv[1..];
    let mut trace = fs::OpenOptions::new()
        .create(true)
        .append(true)
        .open("trace.jsonl")
        .unwrap();
    writeln!(trace, "{}", serde_json::json!({"tool":tool,"args":args})).unwrap();
    let mode = env::var("PDF_STUB_MODE").unwrap_or_default();
    let fail_tool = env::var("PDF_STUB_TOOL").unwrap_or_default();
    let active = fail_tool == tool;
    if active && mode == "cancel" {
        fs::write("ready", process::id().to_string()).unwrap();
        loop {
            thread::sleep(Duration::from_secs(1));
        }
    }
    if active && mode == "error" {
        eprintln!("  controlled failure\nsecond line  ");
        process::exit(7);
    }
    if active && mode == "quiet-error" {
        process::exit(7);
    }
    if active && mode == "no-output" {
        return;
    }
    match tool {
        "pdfinfo" => print!(
            "{}",
            env::var("PDF_STUB_INFO").unwrap_or_else(|_| "Title: fixture\nPages: 4\n".into())
        ),
        "pdfseparate" => {
            assert_eq!(args.len(), 6);
            assert_eq!(args[0], "-f");
            assert_eq!(args[2], "-l");
            assert_eq!(args[1], args[3]);
            let path = args[5].replace("%d", &args[1]);
            if active && mode == "directory" {
                fs::create_dir(path).unwrap();
            } else {
                fs::write(path, format!("%PDF-fixture-page-{}\n", args[1])).unwrap();
            }
        }
        "pdfunite" => {
            let mut bytes = Vec::new();
            for path in &args[..args.len() - 1] {
                bytes.extend(fs::read(path).unwrap());
            }
            fs::write(args.last().unwrap(), bytes).unwrap_or_else(|_| {
                eprintln!("controlled write failure");
                process::exit(7);
            });
        }
        "qpdf" => {
            assert_eq!(args.len(), 4);
            assert_eq!(args[0], "--warning-exit-0");
            assert!(args[3].starts_with("--rotate="));
            let bytes = fs::read(&args[1]).unwrap();
            fs::write(&args[2], bytes).unwrap();
        }
        _ => panic!("unknown fake tool"),
    }
}
