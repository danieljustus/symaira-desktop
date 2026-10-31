//! SEC-002 canary scan on the real Rust binary: a synthetic API key must not
//! appear on stdout, stderr or in any file under the isolated HOME when the
//! provider fails and even echoes the credential back. Go leaks it in the
//! `anthropic-unauthorized-redacted` case of `providers.json`; Rust redacts.
#![cfg(unix)]

use std::{
    fs,
    io::{Read, Write},
    net::TcpListener,
    path::{Path, PathBuf},
    process::Command,
    thread,
};

const CANARY: &str = "port-only-synthetic-cli-canary-7d41";

/// One-shot loopback provider that answers every request with `status` and a
/// body that echoes the credential it received.
fn echo_server(status: u16) -> (String, thread::JoinHandle<()>) {
    let listener = TcpListener::bind("127.0.0.1:0").unwrap();
    let url = format!("http://{}", listener.local_addr().unwrap());
    let handle = thread::spawn(move || {
        let (mut stream, _) = listener.accept().unwrap();
        let mut request = Vec::new();
        let mut chunk = [0_u8; 4096];
        loop {
            let n = stream.read(&mut chunk).unwrap();
            request.extend_from_slice(&chunk[..n]);
            if n == 0 || request.windows(4).any(|w| w == b"\r\n\r\n") {
                break;
            }
        }
        let body = format!("{{\"error\":\"invalid credential {CANARY}\"}}");
        write!(
            stream,
            "HTTP/1.1 {status} Test\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{body}",
            body.len()
        )
        .unwrap();
    });
    (url, handle)
}

fn files_under(dir: &Path, out: &mut Vec<PathBuf>) {
    for entry in fs::read_dir(dir).unwrap() {
        let path = entry.unwrap().path();
        if path.is_dir() {
            files_under(&path, out);
        } else {
            out.push(path);
        }
    }
}

/// Occurrences of the canary in the process output and every file below `home`.
fn leaks(stdout: &[u8], stderr: &[u8], home: &Path) -> usize {
    let count = |bytes: &[u8]| String::from_utf8_lossy(bytes).matches(CANARY).count();
    let mut files = Vec::new();
    files_under(home, &mut files);
    count(stdout)
        + count(stderr)
        + files
            .iter()
            .map(|path| count(&fs::read(path).unwrap_or_default()))
            .sum::<usize>()
}

fn run_transform(status: u16, via: &str) -> (Vec<u8>, Vec<u8>, usize) {
    let root = fs::canonicalize(std::env::temp_dir())
        .unwrap()
        .join(format!(
            "symdesk-canary-{}-{status}-{via}",
            std::process::id()
        ));
    let home = root.join("home");
    fs::create_dir_all(&home).unwrap();
    let (url, server) = echo_server(status);
    let mut command = Command::new(env!("CARGO_BIN_EXE_symdesk"));
    command
        .current_dir(&root)
        .env_clear()
        .env("PATH", "/usr/bin:/bin")
        .env("HOME", &home)
        .env("XDG_CONFIG_HOME", home.join("config"))
        .env("XDG_CACHE_HOME", home.join("cache"))
        .env("XDG_DATA_HOME", home.join("data"))
        .env("SYMDESK_LLM_PROVIDER", "anthropic")
        .env("SYMDESK_ANTHROPIC_URL", &url)
        .args(["--json", "transform", "rewrite", "--text", "hello"]);
    if via == "env" {
        command.env("SYMDESK_LLM_API_KEY", CANARY);
    } else {
        // The key is also legitimately stored in the operator's config file;
        // that file is excluded from the scan below (it is the source).
        fs::create_dir_all(home.join("config/symdesk")).unwrap();
        fs::write(
            home.join("config/symdesk/config.toml"),
            format!("llm_api_key = \"{CANARY}\"\n"),
        )
        .unwrap();
    }
    let output = command.output().unwrap();
    server.join().unwrap();
    if via != "env" {
        fs::remove_file(home.join("config/symdesk/config.toml")).unwrap();
    }
    let found = leaks(&output.stdout, &output.stderr, &home);
    fs::remove_dir_all(&root).unwrap();
    (output.stdout, output.stderr, found)
}

#[test]
fn a_failing_provider_that_echoes_the_key_never_reaches_output_or_disk() {
    for (status, via) in [(401, "env"), (500, "env"), (401, "config"), (429, "config")] {
        let (stdout, stderr, found) = run_transform(status, via);
        let text = String::from_utf8_lossy(&stdout);
        assert!(
            text.contains("Request failed: anthropic:"),
            "{status}/{via}: the provider failure must be reported honestly: {text}"
        );
        assert!(
            text.contains("[REDACTED]"),
            "{status}/{via}: redaction marker: {text}"
        );
        assert_eq!(
            found,
            0,
            "{status}/{via}: canary leaked\nstdout: {text}\nstderr: {}",
            String::from_utf8_lossy(&stderr)
        );
    }
}

#[test]
fn the_scanner_detects_a_planted_canary() {
    let home = fs::canonicalize(std::env::temp_dir())
        .unwrap()
        .join(format!("symdesk-canary-scan-{}", std::process::id()));
    fs::create_dir_all(home.join("nested")).unwrap();
    fs::write(
        home.join("nested/state.json"),
        format!("{{\"k\":\"{CANARY}\"}}"),
    )
    .unwrap();
    assert_eq!(leaks(b"clean", b"also clean", &home), 1, "file leak");
    assert_eq!(
        leaks(CANARY.as_bytes(), b"", &home),
        2,
        "stdout leak plus file"
    );
    assert_eq!(
        leaks(b"", CANARY.as_bytes(), &home),
        2,
        "stderr leak plus file"
    );
    fs::remove_dir_all(home).unwrap();
}
