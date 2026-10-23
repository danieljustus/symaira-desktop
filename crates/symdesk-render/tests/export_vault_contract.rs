//! Replays the Go-recorded vault export oracle
//! (`testdata/port/render/export-vault.json`, issue #1142): `export.Note` and
//! `export.View` over real disposable vaults, covering transclusion expansion,
//! the `*embed ..*` / `*Error loading ..*` lines, path confinement and the
//! Markdown / HTML / PDF-source shapes.
//!
//! Every case builds its own vault under a private directory, runs the port,
//! normalizes the temp paths like the Go oracle and compares the exact output
//! and error text. Error texts carry Unix syscall wording, so the replay is
//! Unix-only (the Go side validates the stored oracle on Windows without
//! re-executing it).
#![cfg(unix)]

use std::{
    collections::BTreeMap,
    fs,
    os::unix::fs::symlink,
    path::PathBuf,
    sync::atomic::{AtomicUsize, Ordering},
};

use symdesk_render::export_vault::{
    MAX_EMBED_EXPANSIONS, ViewSpec, export_note, export_view, resolve_transclusions,
    resolve_transclusions_bounded,
};

/// The recorded corpus size: a shrunken or truncated fixture must fail.
const EXPECTED_CASES: usize = 118;

#[derive(serde::Deserialize)]
#[serde(deny_unknown_fields)]
struct Fixture {
    schema_version: u32,
    oracle: Oracle,
    generated_on: String,
    cases: Vec<Case>,
    #[allow(dead_code)]
    notes: serde_json::Value,
}

#[derive(serde::Deserialize)]
#[serde(deny_unknown_fields)]
struct Oracle {
    commit: String,
    go_version: String,
    sources: BTreeMap<String, String>,
}

#[derive(serde::Deserialize, Clone)]
#[serde(deny_unknown_fields)]
struct File {
    path: String,
    content: String,
    #[serde(default)]
    sparse: u64,
}

#[derive(serde::Deserialize, Clone)]
#[serde(deny_unknown_fields)]
struct Link {
    path: String,
    target: String,
}

#[derive(serde::Deserialize, Clone)]
#[serde(deny_unknown_fields)]
struct View {
    name: String,
    columns: Option<Vec<String>>,
}

#[derive(serde::Deserialize, Clone)]
#[serde(deny_unknown_fields)]
struct Case {
    id: String,
    #[serde(default)]
    files: Vec<File>,
    #[serde(default)]
    dirs: Vec<String>,
    #[serde(default)]
    symlinks: Vec<Link>,
    note: Option<String>,
    view: Option<View>,
    #[serde(default)]
    rows: Vec<BTreeMap<String, String>>,
    format: String,
    output: String,
    error: String,
}

static NEXT: AtomicUsize = AtomicUsize::new(0);

/// A private scratch tree that is removed on drop.
struct Scratch(PathBuf);
impl Scratch {
    fn new() -> Self {
        let base = std::env::temp_dir().join(format!(
            "symdesk-export-vault-{}-{}",
            std::process::id(),
            NEXT.fetch_add(1, Ordering::Relaxed)
        ));
        fs::create_dir_all(&base).unwrap();
        Self(base)
    }
}
impl Drop for Scratch {
    fn drop(&mut self) {
        let _ = fs::remove_dir_all(&self.0);
    }
}

/// Runs one case against the port and returns the normalized (output, error).
fn run(case: &Case) -> (String, String) {
    let scratch = Scratch::new();
    let root = scratch.0.join("vault");
    let outside = scratch.0.join("outside");
    for dir in [root.clone(), outside.clone()]
        .into_iter()
        .chain(case.dirs.iter().map(|dir| root.join(dir)))
    {
        fs::create_dir_all(dir).unwrap();
    }
    fs::write(
        outside.join("secret.md"),
        "---\ntitle: Secret\n---\nTOP-SECRET-OUTSIDE\n",
    )
    .unwrap();
    let resolve = |value: &str| {
        value
            .replace("{ROOT}", root.to_str().unwrap())
            .replace("{OUTSIDE}", outside.to_str().unwrap())
    };
    for file in &case.files {
        let path = root.join(&file.path);
        fs::create_dir_all(path.parent().unwrap()).unwrap();
        if file.sparse > 0 {
            fs::File::create(&path)
                .unwrap()
                .set_len(file.sparse)
                .unwrap();
        } else {
            fs::write(&path, &file.content).unwrap();
        }
    }
    for link in &case.symlinks {
        let path = root.join(&link.path);
        fs::create_dir_all(path.parent().unwrap()).unwrap();
        symlink(resolve(&link.target), &path).unwrap();
    }

    let result = match (&case.note, &case.view) {
        (Some(note), None) => export_note(&root, &resolve(note), &case.format),
        (None, Some(view)) => {
            let rows: Vec<BTreeMap<String, String>> = case
                .rows
                .iter()
                .map(|row| row.iter().map(|(k, v)| (k.clone(), resolve(v))).collect())
                .collect();
            let spec = ViewSpec {
                name: view.name.clone(),
                columns: view.columns.clone().unwrap_or_default(),
            };
            export_view(&root, &spec, &rows, &case.format)
        }
        _ => panic!("case {} must set exactly one of note or view", case.id),
    };

    let canonical_root = fs::canonicalize(&root).unwrap();
    let canonical_outside = fs::canonicalize(&outside).unwrap();
    // Canonical spellings first: the lexical root can be a substring of them.
    let normalize = |value: &str| {
        let mut value = value.to_owned();
        for (from, to) in [
            (&canonical_root, "{ROOT}"),
            (&root, "{ROOT}"),
            (&canonical_outside, "{OUTSIDE}"),
            (&outside, "{OUTSIDE}"),
        ] {
            value = value.replace(from.to_str().unwrap(), to);
        }
        value
    };
    let (output, error) = match result {
        Ok(bytes) => (
            String::from_utf8(bytes).expect("UTF-8 export"),
            String::new(),
        ),
        Err(error) => (String::new(), error),
    };
    let (output, error) = (normalize(&output), normalize(&error));
    for leak in [
        scratch.0.to_str().unwrap(),
        fs::canonicalize(&scratch.0).unwrap().to_str().unwrap(),
    ] {
        assert!(
            !output.contains(leak) && !error.contains(leak),
            "case {} leaks the scratch directory",
            case.id
        );
    }
    assert!(
        !output.contains("TOP-SECRET-OUTSIDE") && !error.contains("TOP-SECRET-OUTSIDE"),
        "case {} exported the outside secret",
        case.id
    );
    (output, error)
}

/// The comparison itself, kept apart so the controls below exercise the same
/// function that gates the replay.
fn check(case: &Case) -> Result<(), String> {
    let (output, error) = run(case);
    if output != case.output {
        return Err(format!(
            "case {}: output differs\n  go:   {:?}\n  rust: {:?}",
            case.id, case.output, output
        ));
    }
    if error != case.error {
        return Err(format!(
            "case {}: error differs\n  go:   {:?}\n  rust: {:?}",
            case.id, case.error, error
        ));
    }
    Ok(())
}

fn fixture() -> Fixture {
    let fixture: Fixture = serde_json::from_str(include_str!(
        "../../../testdata/port/render/export-vault.json"
    ))
    .expect("decode Go-generated export vault fixture");
    let provenance: serde_json::Value =
        serde_json::from_str(include_str!("../../../testdata/port/provenance.json"))
            .expect("decode central port provenance");
    assert_eq!(fixture.schema_version, 1);
    assert_eq!(
        Some(fixture.oracle.commit.as_str()),
        provenance["oracle"]["commit"].as_str()
    );
    assert_eq!(fixture.oracle.go_version, "go1.26.10");
    assert!(fixture.generated_on == "darwin" || fixture.generated_on == "linux");
    assert!(
        fixture
            .oracle
            .sources
            .contains_key("internal/export/export.go")
    );
    assert!(
        fixture
            .oracle
            .sources
            .contains_key("internal/vault/root.go")
    );
    for digest in fixture.oracle.sources.values() {
        assert_eq!(digest.len(), 64);
    }
    assert_eq!(fixture.cases.len(), EXPECTED_CASES);
    fixture
}

#[test]
fn go_export_vault_fixture_matches_exact_output_and_errors() {
    let fixture = fixture();
    let mut failures = Vec::new();
    let mut executed = 0usize;
    let mut with_error = 0usize;
    for case in &fixture.cases {
        executed += 1;
        with_error += usize::from(!case.error.is_empty());
        if let Err(message) = check(case) {
            failures.push(message);
        }
    }
    assert!(failures.is_empty(), "{}", failures.join("\n"));
    // Zero or short runs are not a pass: every recorded case ran, and the
    // error-bearing cases are a real share of the corpus.
    assert_eq!(executed, EXPECTED_CASES);
    assert_eq!(with_error, 14, "recorded error-case count changed");
}

#[test]
fn export_vault_replay_rejects_a_changed_output_or_error() {
    let fixture = fixture();
    let ok = fixture
        .cases
        .iter()
        .find(|case| case.id == "embed-simple")
        .expect("embed-simple");
    let mut wrong_output = ok.clone();
    wrong_output.output.push_str("mutated");
    assert!(check(&wrong_output).unwrap_err().contains("output differs"));
    let failing = fixture
        .cases
        .iter()
        .find(|case| case.id == "note-missing")
        .expect("note-missing");
    let mut wrong_error = failing.clone();
    wrong_error.error.push_str("mutated");
    assert!(check(&wrong_error).unwrap_err().contains("error differs"));
    // A changed input produces a different export, so the recorded output no
    // longer matches.
    let mut changed_input = ok.clone();
    changed_input.files[0].content.push_str("\nextra line\n");
    assert!(check(&changed_input).is_err());
}

#[test]
fn export_vault_recorded_corpus_spans_the_contract_surface() {
    let fixture = fixture();
    let ids: Vec<&str> = fixture.cases.iter().map(|case| case.id.as_str()).collect();
    for needed in [
        "embed-simple",
        "embed-nested-three-levels",
        "embed-traversal",
        "embed-symlink-escape-file",
        "embed-symlink-escape-dir",
        "embed-over-read-limit",
        "note-over-read-limit",
        "note-traversal",
        "note-symlink-escape-dir",
        "view-people-pdf",
        "view-absolute-path-outside-vault",
        "view-missing-note-pdf",
    ] {
        assert!(ids.contains(&needed), "missing recorded case {needed}");
    }
    let formats: std::collections::BTreeSet<&str> = fixture
        .cases
        .iter()
        .map(|case| case.format.as_str())
        .collect();
    for format in ["", "html", "pdf", "markdown", "docx"] {
        assert!(
            formats.contains(format),
            "no recorded case for format {format:?}"
        );
    }
}

/// Go loops forever on an embed cycle; the port is bounded and says so.
#[test]
fn export_vault_embed_cycles_are_bounded_instead_of_hanging() {
    let scratch = Scratch::new();
    let root = scratch.0.join("vault");
    fs::create_dir_all(&root).unwrap();
    fs::write(root.join("a.md"), "start ![[b]]\n").unwrap();
    fs::write(root.join("b.md"), "back ![[a]]\n").unwrap();
    fs::write(root.join("self.md"), "![[self]]\n").unwrap();
    for note in ["a.md", "self.md"] {
        let error = export_note(&root, note, "").expect_err("cycle must not export");
        assert!(error.contains("embed cycle?"), "{note}: {error}");
    }
    // Doubling cycles hit the size bound long before the count bound; a
    // small explicit limit reaches it without expanding tens of megabytes.
    fs::write(
        root.join("big.md"),
        format!("{}![[big]]![[big]]", "x".repeat(4096)),
    )
    .unwrap();
    let error =
        resolve_transclusions_bounded(&root, "![[big]]", 10_000, 64 << 10).expect_err("size bound");
    assert!(
        error.contains("exceeds 65536 bytes (embed cycle?)"),
        "{error}"
    );
    let error =
        resolve_transclusions_bounded(&root, "![[self]]", 50, 64 << 20).expect_err("count bound");
    assert!(
        error.contains("exceeds 50 embeds (embed cycle?)"),
        "{error}"
    );
}

/// Go rescans from offset 0 after every replacement, so a replacement that
/// starts with (or is empty before) `]` completes a `![[x]` candidate that
/// began earlier. Both repros are recorded in the Go corpus as well.
#[test]
fn export_vault_replacement_can_complete_an_earlier_candidate() {
    let scratch = Scratch::new();
    let root = scratch.0.join("vault");
    fs::create_dir_all(&root).unwrap();
    fs::write(root.join("a.md"), "A").unwrap();
    fs::write(root.join("c.md"), "]x").unwrap();
    fs::write(root.join("e.md"), "").unwrap();
    assert_eq!(resolve_transclusions(&root, "![[a]![[c]]").unwrap(), "Ax");
    assert_eq!(resolve_transclusions(&root, "![[a]![[e]]]").unwrap(), "A");
    // Text before the candidate and a later embed are left alone.
    assert_eq!(
        resolve_transclusions(&root, "p ![[a]![[c]] ![[a]]").unwrap(),
        "p Ax A"
    );
}

/// The expansion cap is a declared limit (RENDER-002): a document that needs
/// exactly the cap succeeds, one embed more fails, and only a remaining embed
/// is an error.
#[test]
fn export_vault_expansion_cap_rejects_only_a_remaining_embed() {
    let scratch = Scratch::new();
    let root = scratch.0.join("vault");
    fs::create_dir_all(&root).unwrap();
    fs::write(root.join("x.md"), "x").unwrap();
    fs::write(root.join("outer.md"), "![[x]]").unwrap();
    let ok = |body: &str, cap| resolve_transclusions_bounded(&root, body, cap, 64 << 20);
    assert_eq!(ok("![[x]]![[x]]![[x]]", 3).unwrap(), "xxx");
    let error = ok("![[x]]![[x]]![[x]]![[x]]", 3).unwrap_err();
    assert!(error.contains("exceeds 3 embeds (embed cycle?)"), "{error}");
    // Nested: the second expansion is the last one and leaves nothing.
    assert_eq!(ok("![[outer]]", 2).unwrap(), "x");
    assert!(ok("![[outer]]", 1).is_err());
    assert_eq!(ok("no embeds", 0).unwrap(), "no embeds");
    assert!(ok("![[x]]", 0).is_err());

    // The real cap, through the public entry point.
    let at_cap = "![[x]]".repeat(MAX_EMBED_EXPANSIONS);
    let expanded = resolve_transclusions(&root, &at_cap).unwrap();
    assert_eq!(expanded, "x".repeat(MAX_EMBED_EXPANSIONS));
    let over_cap = "![[x]]".repeat(MAX_EMBED_EXPANSIONS + 1);
    let error = resolve_transclusions(&root, &over_cap).unwrap_err();
    assert!(
        error.contains(&format!(
            "exceeds {MAX_EMBED_EXPANSIONS} embeds (embed cycle?)"
        )),
        "{error}"
    );
}

/// Declared divergence (RENDER-002): the vault parser decodes a body that is
/// not valid UTF-8 lossily, so invalid bytes become U+FFFD where Go keeps the
/// raw bytes. The JSON oracle cannot carry such bytes; this pins the port.
#[test]
fn export_vault_invalid_utf8_body_is_decoded_lossily() {
    let scratch = Scratch::new();
    let root = scratch.0.join("vault");
    fs::create_dir_all(&root).unwrap();
    fs::write(root.join("bad.md"), b"head \xff\xfe tail\n").unwrap();
    fs::write(root.join("host.md"), "![[bad]]").unwrap();
    let direct = export_note(&root, "bad.md", "").unwrap();
    assert_eq!(
        String::from_utf8(direct).unwrap(),
        "# bad\n\nhead \u{FFFD}\u{FFFD} tail\n"
    );
    let embedded = export_note(&root, "host.md", "").unwrap();
    assert_eq!(
        String::from_utf8(embedded).unwrap(),
        "# host\n\nhead \u{FFFD}\u{FFFD} tail\n"
    );
}

/// A FIFO (swapped in after path resolution, or simply present) must be
/// refused without blocking on the open.
#[test]
fn export_vault_fifo_is_refused_without_blocking() {
    let scratch = Scratch::new();
    let root = scratch.0.join("vault");
    fs::create_dir_all(&root).unwrap();
    let fifo = std::ffi::CString::new(root.join("pipe.md").to_str().unwrap()).unwrap();
    // SAFETY: `fifo` is a valid NUL-terminated path for the call's duration.
    assert_eq!(unsafe { libc::mkfifo(fifo.as_ptr(), 0o600) }, 0);
    fs::write(root.join("host.md"), "![[pipe]]").unwrap();

    let (sender, receiver) = std::sync::mpsc::channel();
    let thread_root = root.clone();
    std::thread::spawn(move || {
        let note = export_note(&thread_root, "pipe.md", "");
        let embed = export_note(&thread_root, "host.md", "");
        let _ = sender.send((note, embed));
    });
    let (note, embed) = receiver
        .recv_timeout(std::time::Duration::from_secs(20))
        .expect("reading a FIFO must not block");
    let error = note.expect_err("a FIFO is not a regular file");
    assert!(
        error.contains("vault path is not a regular file"),
        "{error}"
    );
    let embed = String::from_utf8(embed.unwrap()).unwrap();
    assert!(
        embed.contains("*embed error: vault path is not a regular file"),
        "{embed}"
    );
}
