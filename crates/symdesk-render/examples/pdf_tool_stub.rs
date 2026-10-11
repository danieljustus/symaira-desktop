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
        // Cancellation observes existence, so publish the complete PID atomically.
        fs::write("ready.pending", process::id().to_string()).unwrap();
        fs::rename("ready.pending", "ready").unwrap();
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
        "typst" => {
            assert_eq!(args, &["--version"]);
            print!("{}", env::var("PDF_STUB_INFO").unwrap_or_default());
        }
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
            if args[0] == "--check" {
                if active && mode == "check-error" {
                    eprintln!("controlled validation warning");
                    process::exit(3);
                }
                assert!(Path::new(&args[1]).is_file());
            } else if args.len() == 3 {
                assert!(args[2].starts_with("--rotate="));
                fs::write(&args[1], fs::read(&args[0]).unwrap()).unwrap();
            } else {
                assert_eq!(args[1], "--pages");
                assert_eq!(args[2], ".");
                assert_eq!(args[args.len() - 2], "--");
                let mut bytes = Vec::new();
                for pair in args[2..args.len() - 2].as_chunks::<2>().0 {
                    let input = if pair[0] == "." { &args[0] } else { &pair[0] };
                    if pair[1] == "1-z" {
                        bytes.extend(fs::read(input).unwrap());
                    } else {
                        let (first, last) = pair[1].split_once('-').unwrap();
                        for page in first.parse::<usize>().unwrap()..=last.parse().unwrap() {
                            bytes.extend(format!("%PDF-fixture-page-{page}\n").bytes());
                        }
                    }
                }
                fs::write(args.last().unwrap(), bytes).unwrap();
            }
        }
        _ => panic!("unknown fake tool"),
    }
}
