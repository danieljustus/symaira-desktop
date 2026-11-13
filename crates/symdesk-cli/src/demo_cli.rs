//! `symdesk demo init [dir] [--size small|large]` (Go `cmd/symdesk/demo.go`,
//! `internal/demo`): materialises the built-in demo vault.
//!
//! The documents, notes, synthetic PDFs and saved views are byte copies of the
//! Go output (`assets/demo`); the process fixture compares the whole generated
//! tree with Go's, so a drifting asset fails `make cli-admin-bodies-differential`.
//! The large vault is generated exactly like Go's `InitLarge`.
use std::{
    fs, io,
    path::{Component, Path, PathBuf},
    process::ExitCode,
};

use clap::{Arg, ArgMatches, Command};
use serde::Serialize;
use symdesk_vault::{go_path_error, go_quote};

use crate::{emit_error, write_go_json, write_stdout};

/// Go: `demo.LargeVaultDocuments`.
const LARGE_VAULT_DOCUMENTS: usize = 10_000;

/// Documents, PDFs, notes and views in Go's write order.
const ASSETS: &[(&str, &[u8])] = &[
    (
        "documents/Arztbrief_Dr_Beispiel.md",
        include_bytes!("../assets/demo/documents/Arztbrief_Dr_Beispiel.md"),
    ),
    (
        "documents/Brief_Versicherung_Erika.md",
        include_bytes!("../assets/demo/documents/Brief_Versicherung_Erika.md"),
    ),
    (
        "documents/Gehaltsabrechnung_Max_2026-01.md",
        include_bytes!("../assets/demo/documents/Gehaltsabrechnung_Max_2026-01.md"),
    ),
    (
        "documents/Kontoauszug_Sparkasse_2026-01.md",
        include_bytes!("../assets/demo/documents/Kontoauszug_Sparkasse_2026-01.md"),
    ),
    (
        "documents/Rechnung_Musterfirma_2026-01.md",
        include_bytes!("../assets/demo/documents/Rechnung_Musterfirma_2026-01.md"),
    ),
    (
        "documents/Rechnung_Musterfirma_2026-02.md",
        include_bytes!("../assets/demo/documents/Rechnung_Musterfirma_2026-02.md"),
    ),
    (
        "documents/Steuerbescheid_Max_2025.md",
        include_bytes!("../assets/demo/documents/Steuerbescheid_Max_2025.md"),
    ),
    (
        "documents/Stromrechnung_Stadtwerke.md",
        include_bytes!("../assets/demo/documents/Stromrechnung_Stadtwerke.md"),
    ),
    (
        "documents/Versicherungspolice_Erika.md",
        include_bytes!("../assets/demo/documents/Versicherungspolice_Erika.md"),
    ),
    (
        "documents/Vertrag_Dienstleister.md",
        include_bytes!("../assets/demo/documents/Vertrag_Dienstleister.md"),
    ),
    (
        "notes/Hausstand_Mustermann.md",
        include_bytes!("../assets/demo/notes/Hausstand_Mustermann.md"),
    ),
    (
        "notes/Musterfirma_GmbH.md",
        include_bytes!("../assets/demo/notes/Musterfirma_GmbH.md"),
    ),
    (
        "notes/Sparkasse_Beispielstadt.md",
        include_bytes!("../assets/demo/notes/Sparkasse_Beispielstadt.md"),
    ),
    (
        "notes/Steuererklaerung_2026.md",
        include_bytes!("../assets/demo/notes/Steuererklaerung_2026.md"),
    ),
    (
        "notes/Versicherung_AG.md",
        include_bytes!("../assets/demo/notes/Versicherung_AG.md"),
    ),
    (
        "pdfs/Arztbrief_Dr_Beispiel.pdf",
        include_bytes!("../assets/demo/pdfs/Arztbrief_Dr_Beispiel.pdf"),
    ),
    (
        "pdfs/Brief_Versicherung_Erika.pdf",
        include_bytes!("../assets/demo/pdfs/Brief_Versicherung_Erika.pdf"),
    ),
    (
        "pdfs/Gehaltsabrechnung_Max_2026-01.pdf",
        include_bytes!("../assets/demo/pdfs/Gehaltsabrechnung_Max_2026-01.pdf"),
    ),
    (
        "pdfs/Kontoauszug_Sparkasse_2026-01.pdf",
        include_bytes!("../assets/demo/pdfs/Kontoauszug_Sparkasse_2026-01.pdf"),
    ),
    (
        "pdfs/Rechnung_Musterfirma_2026-01.pdf",
        include_bytes!("../assets/demo/pdfs/Rechnung_Musterfirma_2026-01.pdf"),
    ),
    (
        "pdfs/Rechnung_Musterfirma_2026-02.pdf",
        include_bytes!("../assets/demo/pdfs/Rechnung_Musterfirma_2026-02.pdf"),
    ),
    (
        "pdfs/Steuerbescheid_Max_2025.pdf",
        include_bytes!("../assets/demo/pdfs/Steuerbescheid_Max_2025.pdf"),
    ),
    (
        "pdfs/Stromrechnung_Stadtwerke.pdf",
        include_bytes!("../assets/demo/pdfs/Stromrechnung_Stadtwerke.pdf"),
    ),
    (
        "pdfs/Versicherungspolice_Erika.pdf",
        include_bytes!("../assets/demo/pdfs/Versicherungspolice_Erika.pdf"),
    ),
    (
        "pdfs/Vertrag_Dienstleister.pdf",
        include_bytes!("../assets/demo/pdfs/Vertrag_Dienstleister.pdf"),
    ),
    (
        ".symdesk/views.json",
        include_bytes!("../assets/demo/.symdesk/views.json"),
    ),
];

pub fn cli() -> Command {
    Command::new("demo").subcommand(
        Command::new("init")
            .arg(Arg::new("dir").num_args(0..).hide(true))
            .arg(Arg::new("size").long("size").num_args(1)),
    )
}

#[derive(Serialize)]
struct Created<'a> {
    path: &'a str,
    status: &'a str,
}

pub fn run(command: &ArgMatches, output_json: bool) -> ExitCode {
    let Some(("init", init)) = command.subcommand() else {
        return crate::process_exit(crate::CoreExitCode::Ok);
    };
    let dir = init
        .get_many::<String>("dir")
        .and_then(|mut values| values.next())
        .map_or("symdesk-demo", String::as_str);
    let size = init
        .get_one::<String>("size")
        .map_or("small", String::as_str);
    if let Err(error) = init_size(dir, size) {
        return emit_error(error, output_json);
    }
    let absolute = absolute(Path::new(dir));
    let absolute = absolute.to_string_lossy();
    if output_json {
        return write_go_json(&Created {
            path: &absolute,
            status: "ok",
        });
    }
    write_stdout(format!(
        "Demo vault created at {absolute}\nNext steps:\n  SYMDESK_VAULT={absolute} symdesk index\n  SYMDESK_VAULT={absolute} symdesk docs list\n"
    ))
}

/// Go `demo.InitSize`.
fn init_size(dir: &str, size: &str) -> Result<(), String> {
    match size.to_lowercase().as_str() {
        "" | "small" => materialize(dir, 0),
        "large" => materialize(dir, LARGE_VAULT_DOCUMENTS),
        _ => Err(format!(
            "unknown demo size {}: use small or large",
            go_quote(size)
        )),
    }
}

/// Go `filepath.Abs`: the working directory joined and lexically cleaned.
fn absolute(path: &Path) -> PathBuf {
    let joined = if path.is_absolute() {
        path.to_path_buf()
    } else {
        std::env::current_dir().unwrap_or_default().join(path)
    };
    let mut cleaned = PathBuf::new();
    for component in joined.components() {
        match component {
            Component::CurDir => {}
            Component::ParentDir => {
                cleaned.pop();
            }
            other => cleaned.push(other.as_os_str()),
        }
    }
    cleaned
}

fn materialize(dir: &str, generated_documents: usize) -> Result<(), String> {
    let root = absolute(Path::new(dir));
    match fs::read_dir(&root) {
        Ok(mut entries) => {
            if entries.next().is_some() {
                return Err(format!(
                    "refusing to overwrite non-empty directory: {}",
                    root.display()
                ));
            }
        }
        Err(error) if error.kind() == io::ErrorKind::NotFound => {}
        Err(error) => {
            return Err(format!(
                "read directory: {}",
                go_path_error("open", &root, &error)
            ));
        }
    }

    for directory in [
        root.clone(),
        root.join("documents"),
        root.join("notes"),
        root.join(".symdesk"),
        root.join("pdfs"),
    ] {
        fs::create_dir_all(&directory).map_err(|error| {
            format!(
                "create directory {}: {}",
                directory.display(),
                go_path_error("mkdir", &directory, &error)
            )
        })?;
    }

    let write = |relative: &str, bytes: &[u8], what: &str| -> Result<(), String> {
        let target = root.join(relative);
        fs::write(&target, bytes)
            .map_err(|error| format!("write {what}: {}", go_path_error("open", &target, &error)))
    };
    // Documents (each followed by its PDF) come first, then the generated
    // benchmark documents, the notes and the saved views, like Go.
    for (relative, bytes) in ASSETS
        .iter()
        .filter(|(name, _)| name.starts_with("documents/"))
    {
        let name = relative
            .trim_start_matches("documents/")
            .trim_end_matches(".md");
        write(relative, bytes, &format!("document {name}"))?;
        let pdf = format!("pdfs/{name}.pdf");
        let pdf_bytes = ASSETS
            .iter()
            .find(|(candidate, _)| *candidate == pdf)
            .map(|(_, bytes)| *bytes)
            .unwrap_or_default();
        write(&pdf, pdf_bytes, &format!("pdf {name}"))?;
    }
    for index in 0..generated_documents {
        let number = index + 1;
        let name = format!("benchmark-{number:05}");
        let body = format!(
            "---\ntitle: \"Benchmark document {number:05}\"\ncreated: \"2026-01-01T00:00:00Z\"\ntags: [benchmark, generated]\ndocument_type: \"benchmark\"\nstatus: \"open\"\n---\n\n# Benchmark document {number:05}\n\nDeterministic benchmark content for indexing and full-text search. Group {}.\n",
            index % 100
        );
        write(
            &format!("documents/{name}.md"),
            body.as_bytes(),
            &format!("generated document {name}"),
        )?;
    }
    for (relative, bytes) in ASSETS.iter().filter(|(name, _)| name.starts_with("notes/")) {
        let name = relative
            .trim_start_matches("notes/")
            .trim_end_matches(".md");
        write(relative, bytes, &format!("note {name}"))?;
    }
    for (relative, bytes) in ASSETS
        .iter()
        .filter(|(name, _)| *name == ".symdesk/views.json")
    {
        write(relative, bytes, "views")?;
    }
    Ok(())
}
