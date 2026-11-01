//! `symdesk clip <url>` (Go `cmd/symdesk/clip.go`, `internal/service/service.go`
//! `clipURL`/`clipTitle`/`NoteClip`): render a URL with the `symbrowse`
//! sibling and save the Markdown as a note.
use std::{
    io::Read,
    path::Path,
    process::{Command as Process, ExitCode, Stdio},
    time::{Duration, Instant},
};

use clap::{Arg, Command};
use symdesk_core::compose::Resolver;
use symdesk_index::IndexedDocument;
use symdesk_vault::go_quote;

use crate::{emit_error, ingest_cli::init_service_deps, write_go_json, write_stdout};

pub fn cli() -> Command {
    Command::new("clip").arg(Arg::new("args").num_args(0..))
}

pub fn run(command: &clap::ArgMatches, vault: Option<&str>, json: bool) -> ExitCode {
    let url = command
        .get_many::<String>("args")
        .into_iter()
        .flatten()
        .next()
        .cloned()
        .unwrap_or_default();
    match execute(&url, vault) {
        Ok(path) => {
            if json {
                write_go_json(&serde_json::json!({ "path": path, "status": "ok" }))
            } else {
                write_stdout(format!("map[path:{path} status:ok]\n"))
            }
        }
        Err(error) => emit_error(error, json),
    }
}

/// Go `clipURL`: symbrowse is the only web clipper.
fn clip_url(url: &str) -> Result<String, String> {
    let resolver = Resolver::from_environment().map_err(|error| error.to_string())?;
    let Ok(binary) = resolver.resolve("symbrowse") else {
        return Err(
            "no web clipper found: install symbrowse with brew install danieljustus/tap/symbrowse"
                .to_owned(),
        );
    };
    let mut child = Process::new(&binary)
        .args(["read", url])
        .stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::null())
        .spawn()
        .map_err(|error| format!("symbrowse failed: {error}"))?;
    let mut stdout = child.stdout.take().expect("piped stdout");
    let reader = std::thread::spawn(move || {
        let mut out = Vec::new();
        let _ = stdout.read_to_end(&mut out);
        out
    });
    let deadline = Instant::now() + Duration::from_secs(30);
    let status = loop {
        match child.try_wait() {
            Ok(Some(status)) => break status,
            Ok(None) if Instant::now() >= deadline => {
                let _ = child.kill();
                let _ = child.wait();
                let _ = reader.join();
                return Err("symbrowse failed: signal: killed".to_owned());
            }
            Ok(None) => std::thread::sleep(Duration::from_millis(5)),
            Err(error) => return Err(format!("symbrowse failed: {error}")),
        }
    };
    let out = reader.join().unwrap_or_default();
    if !status.success() {
        use std::os::unix::process::ExitStatusExt;
        return Err(match (status.code(), status.signal()) {
            (Some(code), _) => format!("symbrowse failed: exit status {code}"),
            (None, Some(signal)) => format!("symbrowse failed: signal: {}", signal_name(signal)),
            _ => "symbrowse failed: unknown exit".to_owned(),
        });
    }
    Ok(String::from_utf8_lossy(&out).into_owned())
}

fn signal_name(signal: i32) -> String {
    match signal {
        1 => "hangup".to_owned(),
        2 => "interrupt".to_owned(),
        6 => "aborted".to_owned(),
        9 => "killed".to_owned(),
        11 => "segmentation fault".to_owned(),
        13 => "broken pipe".to_owned(),
        15 => "terminated".to_owned(),
        other => format!("signal {other}"),
    }
}

/// Go `clipTitle`.
fn clip_title(body: &str, url: &str) -> String {
    let lines: Vec<&str> = body.split('\n').collect();
    if lines.first().is_some_and(|line| line.trim() == "---") {
        for line in &lines[1..] {
            if line.trim() == "---" {
                break;
            }
            if let Some(rest) = line.strip_prefix("title:") {
                let title = rest.trim().trim_matches('"');
                if !title.is_empty() {
                    return title.to_owned();
                }
            }
        }
    }
    for line in &lines {
        if let Some(heading) = line.strip_prefix("# ") {
            return heading.trim().to_owned();
        }
    }
    url.to_owned()
}

fn execute(url: &str, vault: Option<&str>) -> Result<String, String> {
    let (root, mut sidecar) = init_service_deps(vault)?;
    let outcome = note_clip(&root, &mut sidecar, url);
    if let Err(error) = sidecar.close() {
        eprintln!("warning: failed to close sidecar database: {error}");
    }
    outcome
}

/// Go `Service.NoteClip`.
fn note_clip(
    root: &Path,
    sidecar: &mut symdesk_index::Sidecar,
    url: &str,
) -> Result<String, String> {
    let body = clip_url(url)?;
    let title = clip_title(&body, url);
    let now = {
        let now = time::OffsetDateTime::now_utc();
        symdesk_ingest::writer::rfc3339_nano(now.replace_nanosecond(0).unwrap_or(now))
    };
    let note_title = format!("Clipped: {title}");
    let safe_title = note_title.replace(['/', '\\'], "-");
    let file_name = format!("{}.md", safe_title.replace(' ', "_"));
    let path = symdesk_vault::secure_path(root, &file_name).map_err(|error| error.to_string())?;
    let content = format!(
        "---\ntitle: {}\ncreated: {}\nsource_uri: {}\ningested_at: {}\ntags: []\n---\n\n{body}",
        go_quote(&note_title),
        go_quote(&now),
        go_quote(url),
        go_quote(&now),
    );
    crate::mutate_cli::snapshot(root, &path);
    std::fs::write(&path, &content).map_err(|error| {
        format!(
            "failed to write clipped file: {}",
            symdesk_vault::go_path_error("open", &path, &error)
        )
    })?;
    let mut doc = crate::mutate_cli::parse(&path).map_err(|error| error.clone())?;
    doc.path = symdesk_index::document_storage_key(&path)
        .map_err(|error| error.to_string())?
        .to_string_lossy()
        .into_owned();
    doc.sha256 = symdesk_vault::sha256_hex(content.as_bytes());
    let indexed = IndexedDocument::from_vault(&doc, None).map_err(|error| error.to_string())?;
    sidecar
        .index_document(&indexed)
        .map_err(|error| format!("failed to index clipped file: {error}"))?;
    Ok(file_name)
}
