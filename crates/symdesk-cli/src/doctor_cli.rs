//! `symdesk doctor` (Go `cmd/symdesk/doctor.go`): one health report over the
//! vault, contract/ASN integrity, archive and mail-config paths, sidecar,
//! hybrid index, sibling tools, sync conflicts, AI provider and agent results.
//!
//! Go prints the text report by ranging over a map, so the order of its
//! sections is random; this port prints them in key order.
use std::{
    collections::BTreeMap,
    fs,
    path::{Path, PathBuf},
    process::ExitCode,
};

use clap::{Arg, Command};
use noyalib::Value;
use serde::Serialize;
use symdesk_core::compose::{ResolveOrigin, Resolver, ToolInstallation};
use symdesk_index::open_for_vault;
use symdesk_render::export_vault::read_in_root;
use symdesk_service::secrets;
use symdesk_vault::{VaultError, parse_bytes, walk_markdown};

use crate::{
    CoreExitCode, go_escape_json, lexical_clean, process_exit, resolve_vault_root, write_stderr,
    write_stdout,
};

const SIBLING_TOOLS: [&str; 3] = ["symmemory", "symvault", "symbrowse"];
const INSTALL_HINTS: [(&str, &str); 3] = [
    ("symmemory", "brew install danieljustus/tap/symmemory"),
    ("symvault", "brew install danieljustus/tap/symvault"),
    ("symbrowse", "brew install danieljustus/tap/symbrowse"),
];
const DEFAULT_ANTHROPIC_MODEL: &str = "claude-sonnet-5";

pub fn command() -> Command {
    Command::new("doctor").arg(Arg::new("extra").num_args(0..).hide(true))
}

/// A report value that renders both as Go `json.Marshal` and as `fmt` `%v`.
enum Node {
    Str(String),
    Int(i64),
    Bool(bool),
    List(Vec<Node>),
    Map(BTreeMap<String, Node>),
    /// Pre-rendered JSON and `%v` text (structs with their own field order).
    Raw {
        json: String,
        text: String,
    },
}

impl Node {
    fn text_map<const N: usize>(entries: [(&str, String); N]) -> Self {
        Self::Map(
            entries
                .into_iter()
                .map(|(key, value)| (key.to_owned(), Self::Str(value)))
                .collect(),
        )
    }

    fn json(&self) -> String {
        match self {
            Self::Str(text) => serde_json::to_string(text).unwrap_or_default(),
            Self::Int(number) => number.to_string(),
            Self::Bool(flag) => flag.to_string(),
            Self::List(items) if items.is_empty() => "[]".to_owned(),
            Self::List(items) => format!(
                "[{}]",
                items.iter().map(Self::json).collect::<Vec<_>>().join(",")
            ),
            Self::Map(entries) => format!(
                "{{{}}}",
                entries
                    .iter()
                    .map(|(key, value)| format!(
                        "{}:{}",
                        serde_json::to_string(key).unwrap_or_default(),
                        value.json()
                    ))
                    .collect::<Vec<_>>()
                    .join(",")
            ),
            Self::Raw { json, .. } => json.clone(),
        }
    }

    fn text(&self) -> String {
        match self {
            Self::Str(text) => text.clone(),
            Self::Int(number) => number.to_string(),
            Self::Bool(flag) => flag.to_string(),
            Self::List(items) => format!(
                "[{}]",
                items.iter().map(Self::text).collect::<Vec<_>>().join(" ")
            ),
            Self::Map(entries) => format!(
                "map[{}]",
                entries
                    .iter()
                    .map(|(key, value)| format!("{key}:{}", value.text()))
                    .collect::<Vec<_>>()
                    .join(" ")
            ),
            Self::Raw { text, .. } => text.clone(),
        }
    }
}

#[derive(Serialize)]
struct Diagnostic {
    path: String,
    message: String,
}

#[derive(Serialize)]
struct Duplicate {
    asn: i64,
    paths: Vec<String>,
}

/// Go `vault.ASNReport`.
#[derive(Serialize, Default)]
struct AsnReport {
    files_scanned: i64,
    assigned: i64,
    malformed: Option<Vec<Diagnostic>>,
    duplicates: Option<Vec<Duplicate>>,
    parse_errors: Option<Vec<Diagnostic>>,
}

impl AsnReport {
    fn healthy(&self) -> bool {
        self.malformed.is_none() && self.duplicates.is_none() && self.parse_errors.is_none()
    }
}

/// Go `vault.ScanASNs`.
fn scan_asns(root: &Path) -> Result<AsnReport, String> {
    let mut report = AsnReport::default();
    let canonical = fs::canonicalize(root).map_err(|error| format!("walk vault: {error}"))?;
    let mut assignments: BTreeMap<i64, Vec<String>> = BTreeMap::new();
    let mut malformed = Vec::new();
    let mut parse_errors = Vec::new();
    for relative in walk_markdown(root).map_err(|error| format!("walk vault: {error}"))? {
        report.files_scanned += 1;
        let rel = relative.to_string_lossy().replace('\\', "/");
        let path = canonical.join(&relative);
        let bytes = match read_in_root(root, &path) {
            Ok(bytes) => bytes,
            Err(message) => {
                parse_errors.push(Diagnostic { path: rel, message });
                continue;
            }
        };
        match parse_bytes(&path.to_string_lossy(), &bytes) {
            Ok(document) => {
                if let Some(asn) = document.asn {
                    report.assigned += 1;
                    assignments.entry(asn).or_default().push(rel);
                }
            }
            Err(VaultError::Asn { reason, .. }) => malformed.push(Diagnostic {
                path: rel,
                message: reason.to_owned(),
            }),
            Err(error) => parse_errors.push(Diagnostic {
                path: rel,
                message: error.to_string(),
            }),
        }
    }
    let mut duplicates = Vec::new();
    for (asn, mut paths) in assignments {
        if paths.len() > 1 {
            paths.sort();
            duplicates.push(Duplicate { asn, paths });
        }
    }
    malformed.sort_by(|left, right| left.path.cmp(&right.path));
    parse_errors.sort_by(|left, right| left.path.cmp(&right.path));
    report.malformed = (!malformed.is_empty()).then_some(malformed);
    report.duplicates = (!duplicates.is_empty()).then_some(duplicates);
    report.parse_errors = (!parse_errors.is_empty()).then_some(parse_errors);
    Ok(report)
}

#[derive(Serialize, Clone)]
struct WorkspaceTool {
    name: String,
    binary: String,
    found: bool,
    #[serde(skip_serializing_if = "String::is_empty")]
    path: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    version: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    managed_version: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    origin: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    probe_error: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    install_hint: String,
    managed_found: bool,
    path_found: bool,
    #[serde(skip_serializing_if = "String::is_empty")]
    path_version: String,
    managed_shadows_newer: bool,
}

impl WorkspaceTool {
    /// Go `%v` of the struct: fields in declaration order.
    fn go_text(&self) -> String {
        format!(
            "{{{} {} {} {} {} {} {} {} {} {} {} {} {}}}",
            self.name,
            self.binary,
            self.found,
            self.path,
            self.version,
            self.managed_version,
            self.origin,
            self.probe_error,
            self.install_hint,
            self.managed_found,
            self.path_found,
            self.path_version,
            self.managed_shadows_newer
        )
    }
}

fn parse_tool_version(version: &str) -> Option<Vec<u64>> {
    let mut version = version.trim();
    version = version.strip_prefix('v').unwrap_or(version);
    if version.is_empty() {
        return None;
    }
    if let Some(build) = version.find('+') {
        version = &version[..build];
    }
    if let Some(prerelease) = version.find('-') {
        version = &version[..prerelease];
    }
    version
        .split('.')
        .map(|part| {
            if part.is_empty() || !part.bytes().all(|byte| byte.is_ascii_digit()) {
                None
            } else {
                part.parse().ok()
            }
        })
        .collect()
}

/// Go `toolVersionIsNewer`.
fn tool_version_is_newer(candidate: &str, current: &str) -> bool {
    let (Some(candidate), Some(current)) =
        (parse_tool_version(candidate), parse_tool_version(current))
    else {
        return false;
    };
    for index in 0..candidate.len().max(current.len()) {
        let left = candidate.get(index).copied().unwrap_or(0);
        let right = current.get(index).copied().unwrap_or(0);
        if left != right {
            return left > right;
        }
    }
    false
}

fn inspect_workspace_tools(resolver: &Resolver) -> Vec<WorkspaceTool> {
    SIBLING_TOOLS
        .iter()
        .map(|name| {
            let provenance = resolver.inspect_tool(name);
            let installation = |install: &ToolInstallation| install.found;
            let mut tool = WorkspaceTool {
                name: (*name).to_owned(),
                binary: (*name).to_owned(),
                found: installation(&provenance.effective),
                path: String::new(),
                version: String::new(),
                managed_version: String::new(),
                origin: String::new(),
                probe_error: String::new(),
                install_hint: INSTALL_HINTS
                    .iter()
                    .find(|(tool, _)| tool == name)
                    .map(|(_, hint)| (*hint).to_owned())
                    .unwrap_or_default(),
                managed_found: provenance.managed.found,
                path_found: provenance.path_install.found,
                path_version: String::new(),
                managed_shadows_newer: false,
            };
            if provenance.effective.found {
                tool.path.clone_from(&provenance.effective.path);
                tool.version.clone_from(&provenance.effective.version);
                tool.probe_error
                    .clone_from(&provenance.effective.probe_error);
            }
            if provenance.managed.found {
                tool.managed_version.clone_from(&provenance.managed.version);
            }
            if provenance.path_install.found
                && provenance.path_install.path != provenance.managed.path
            {
                tool.path_version
                    .clone_from(&provenance.path_install.version);
            }
            tool.origin = match provenance.origin {
                Some(ResolveOrigin::SymairaBinEnv) => "override",
                Some(ResolveOrigin::ManagedRuntime) => "managed",
                Some(ResolveOrigin::Path) => "path",
                None => "",
            }
            .to_owned();
            if tool.origin == "managed" && tool.managed_found && !tool.path_version.is_empty() {
                tool.managed_shadows_newer =
                    tool_version_is_newer(&tool.path_version, &tool.managed_version);
            }
            tool
        })
        .collect()
}

fn legacy_tool_origin(origin: &str) -> &'static str {
    match origin {
        "override" => "symaira_bin_env",
        "managed" => "managed_runtime",
        "path" => "path",
        _ => "not_found",
    }
}

/// Go `checkArchiveInVault`: the archive path and whether it is inside the vault.
fn check_archive_in_vault(
    environment: &BTreeMap<String, String>,
    vault: &Path,
) -> Result<(String, bool), String> {
    let mut archive = crate::config_paths::ingest_archive_path(environment).unwrap_or_default();
    if archive.is_empty()
        && let Some(path) =
            std::env::var_os("SYMINGEST_ARCHIVE_PATH").filter(|value| !value.is_empty())
    {
        archive = path.to_string_lossy().into_owned();
    }
    if archive.is_empty() {
        let data = match std::env::var("XDG_DATA_HOME") {
            Ok(value) if !value.is_empty() => PathBuf::from(value),
            _ => PathBuf::from(
                std::env::var("HOME")
                    .map_err(|_| "user home dir: $HOME is not defined".to_owned())?,
            )
            .join(".local")
            .join("share"),
        };
        archive = data
            .join("symingest")
            .join("archive")
            .to_string_lossy()
            .into_owned();
    }
    let cwd = std::env::current_dir().map_err(|error| error.to_string())?;
    let absolute = |path: &Path| lexical_clean(&cwd.join(path));
    let archive_abs = absolute(Path::new(&archive));
    let vault_abs = absolute(vault);
    let inside = match archive_abs.strip_prefix(&vault_abs) {
        Ok(relative) => {
            let relative = relative.to_string_lossy();
            relative.is_empty() || !relative.starts_with("..")
        }
        Err(_) => false,
    };
    Ok((archive, inside))
}

struct ArchivePaths {
    unresolved: Vec<BTreeMap<String, String>>,
    unresolved_count: i64,
    absolute_count: i64,
}

/// Go `filepath.Clean` for a relative slash path (keeps leading `..`).
fn go_clean_relative(path: &str) -> String {
    let mut parts: Vec<&str> = Vec::new();
    for part in path.split('/') {
        match part {
            "" | "." => {}
            ".." => {
                if parts.last().is_some_and(|last| *last != "..") {
                    parts.pop();
                } else {
                    parts.push("..");
                }
            }
            other => parts.push(other),
        }
    }
    if parts.is_empty() {
        ".".to_owned()
    } else {
        parts.join("/")
    }
}

/// Go `checkArchivePathsInVault`.
fn check_archive_paths(root: &Path) -> Result<ArchivePaths, String> {
    let mut result = ArchivePaths {
        unresolved: Vec::new(),
        unresolved_count: 0,
        absolute_count: 0,
    };
    let canonical = fs::canonicalize(root).map_err(|error| format!("walk vault: {error}"))?;
    let mut record = |note: &Path, archive: &str, reason: &str| {
        result.unresolved_count += 1;
        if result.unresolved.len() < 20 {
            result.unresolved.push(BTreeMap::from([
                ("note".to_owned(), note.to_string_lossy().into_owned()),
                ("archive_path".to_owned(), archive.to_owned()),
                ("reason".to_owned(), reason.to_owned()),
            ]));
        }
    };
    let mut absolute_count = 0;
    for relative in walk_markdown(root).map_err(|error| format!("walk vault: {error}"))? {
        let path = canonical.join(&relative);
        let note = root.join(&relative);
        let Ok(bytes) = read_in_root(root, &path) else {
            continue;
        };
        let Ok(document) = parse_bytes(&path.to_string_lossy(), &bytes) else {
            continue;
        };
        let Some(Value::String(raw)) = document.frontmatter.get("archive_path") else {
            continue;
        };
        if raw.is_empty() {
            continue;
        }
        if raw.starts_with('/') {
            absolute_count += 1;
            match fs::metadata(raw) {
                Err(_) => record(&note, raw, "missing absolute archive"),
                Ok(info) if info.is_dir() => record(&note, raw, "archive_path is a directory"),
                Ok(_) => {}
            }
            continue;
        }
        let clean_text = go_clean_relative(raw);
        let clean = PathBuf::from(&clean_text);
        if clean_text == ".." || clean_text.starts_with("../") || clean.is_absolute() {
            record(&note, raw, "archive_path escapes the vault");
            continue;
        }
        let candidate = root.join(&clean);
        let inside = fs::canonicalize(&candidate)
            .ok()
            .is_some_and(|resolved| resolved.starts_with(&canonical));
        match fs::metadata(&candidate) {
            Ok(info) if inside => {
                if info.is_dir() {
                    record(&note, raw, "archive_path is a directory");
                }
            }
            _ => record(&note, raw, "missing vault-relative archive"),
        }
    }
    result.absolute_count = absolute_count;
    Ok(result)
}

/// Go `ai.ResultsSize`: bytes and regular-file count below `root`.
fn results_size(root: &Path) -> Result<(i64, i64), String> {
    fn walk(directory: &Path, bytes: &mut i64, files: &mut i64) -> std::io::Result<()> {
        let mut entries: Vec<_> = fs::read_dir(directory)?.collect::<Result<_, _>>()?;
        entries.sort_by_key(fs::DirEntry::file_name);
        for entry in entries {
            let kind = entry.file_type()?;
            if kind.is_dir() {
                walk(&entry.path(), bytes, files)?;
            } else if kind.is_file() {
                *bytes += i64::try_from(entry.metadata()?.len()).unwrap_or(i64::MAX);
                *files += 1;
            }
        }
        Ok(())
    }
    let (mut bytes, mut files) = (0, 0);
    match walk(root, &mut bytes, &mut files) {
        Ok(()) => Ok((bytes, files)),
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => Ok((0, 0)),
        Err(error) => Err(symdesk_vault::go_path_error("open", root, &error)),
    }
}

/// Go `configkit.DefaultPath("symingest")`, selected by CoreKit.
fn mail_config_path() -> String {
    let environment = ["XDG_CONFIG_HOME", "HOME"]
        .into_iter()
        .filter_map(|key| std::env::var(key).ok().map(|value| (key.to_owned(), value)))
        .collect::<std::collections::BTreeMap<_, _>>();
    symdesk_core::config::default_config_file("symingest", &environment)
}

fn status_error(message: impl Into<String>) -> Node {
    Node::text_map([("status", "error".to_owned()), ("message", message.into())])
}

pub fn run(vault_flag: Option<&str>, output_json: bool) -> ExitCode {
    let (config, environment) = match crate::config_paths::load_root_config() {
        Ok(loaded) => loaded,
        Err(error) => {
            return write_stderr(
                &format!("failed to load config: {error}\n"),
                CoreExitCode::Config,
            );
        }
    };
    let mut results: BTreeMap<String, Node> = BTreeMap::new();
    let mut all_ok = true;

    // 1. Vault.
    let root = match resolve_vault_root(vault_flag) {
        Ok(root) => {
            results.insert(
                "vault".to_owned(),
                Node::text_map([
                    ("status", "ok".to_owned()),
                    ("path", root.to_string_lossy().into_owned()),
                ]),
            );
            Some(root)
        }
        Err(error) => {
            results.insert("vault".to_owned(), status_error(error));
            all_ok = false;
            None
        }
    };

    // 2. Contract and ASN integrity.
    let mut asn_report: Option<AsnReport> = None;
    if let Some(root) = &root {
        match scan_asns(root) {
            Err(error) => {
                results.insert("contract".to_owned(), status_error(error));
                all_ok = false;
            }
            Ok(report) => {
                let broken = report.parse_errors.is_some();
                if broken {
                    all_ok = false;
                }
                results.insert(
                    "contract".to_owned(),
                    Node::Map(BTreeMap::from([
                        (
                            "status".to_owned(),
                            Node::Str(if broken { "error" } else { "ok" }.to_owned()),
                        ),
                        ("files_found".to_owned(), Node::Int(report.files_scanned)),
                    ])),
                );
                if !report.healthy() {
                    all_ok = false;
                }
                results.insert(
                    "asn".to_owned(),
                    Node::Raw {
                        json: serde_json::to_string(&report).unwrap_or_default(),
                        text: String::new(),
                    },
                );
                asn_report = Some(report);
            }
        }
    }

    // 2b. Archive coverage.
    if let Some(root) = &root {
        match check_archive_in_vault(&environment, root) {
            Err(error) => {
                results.insert("archive_backup".to_owned(), status_error(error));
                all_ok = false;
            }
            Ok((path, true)) => {
                results.insert(
                    "archive_backup".to_owned(),
                    Node::text_map([("status", "ok".to_owned()), ("path", path)]),
                );
            }
            Ok((path, false)) => {
                results.insert(
                    "archive_backup".to_owned(),
                    Node::text_map([
                        ("status", "warn".to_owned()),
                        ("path", path),
                        (
                            "message",
                            "archive is outside the vault; include it separately in backups"
                                .to_owned(),
                        ),
                    ]),
                );
            }
        }
    }

    // 2b2. Mail configuration path.
    results.insert(
        "mail_config".to_owned(),
        Node::text_map([("status", "ok".to_owned()), ("path", mail_config_path())]),
    );

    // 2c. Note-level archive_path resolution.
    if let Some(root) = &root {
        match check_archive_paths(root) {
            Err(error) => {
                results.insert("archive_paths".to_owned(), status_error(error));
                all_ok = false;
            }
            Ok(paths) => {
                let mut entry = BTreeMap::from([
                    ("status".to_owned(), Node::Str("ok".to_owned())),
                    (
                        "unresolved_count".to_owned(),
                        Node::Int(paths.unresolved_count),
                    ),
                    ("absolute_count".to_owned(), Node::Int(paths.absolute_count)),
                    ("vault_relative_ok".to_owned(), Node::Bool(true)),
                ]);
                let mut messages = Vec::new();
                if paths.unresolved_count > 0 {
                    entry.insert("status".to_owned(), Node::Str("warn".to_owned()));
                    entry.insert(
                        "unresolved".to_owned(),
                        Node::List(
                            paths
                                .unresolved
                                .into_iter()
                                .map(|item| {
                                    Node::Map(
                                        item.into_iter()
                                            .map(|(key, value)| (key, Node::Str(value)))
                                            .collect(),
                                    )
                                })
                                .collect(),
                        ),
                    );
                    messages.push("one or more notes have an archive_path that does not resolve under the vault; re-ingest or move the original back into the vault");
                }
                if paths.absolute_count > 0 {
                    entry.insert("status".to_owned(), Node::Str("warn".to_owned()));
                    messages.push("one or more notes still use absolute archive_path values from a pre-#660 install; re-ingest the affected notes to rewrite the path in the vault-relative form");
                }
                if !messages.is_empty() {
                    entry.insert("message".to_owned(), Node::Str(messages.join("; ")));
                }
                results.insert("archive_paths".to_owned(), Node::Map(entry));
            }
        }
    }

    // 3. Sidecar.
    let sidecar_root = root.clone().unwrap_or_default();
    let sidecar = open_for_vault(&sidecar_root);
    match &sidecar {
        Err(error) => {
            results.insert("sidecar".to_owned(), status_error(error.to_string()));
            all_ok = false;
        }
        Ok(db) => match db.check_integrity() {
            Err(error) => {
                results.insert("sidecar".to_owned(), status_error(error.to_string()));
                all_ok = false;
            }
            Ok(()) => {
                results.insert(
                    "sidecar".to_owned(),
                    Node::text_map([("status", "ok".to_owned())]),
                );
            }
        },
    }

    // 3b. Hybrid search index.
    match crate::index_cli::retrieval_health() {
        Err(error) => {
            results.insert("retrieval".to_owned(), status_error(error));
            all_ok = false;
        }
        Ok(health) => {
            let mut status = "ok";
            if !health.backend_available || health.pending_chunk_count > 0 {
                status = "warn";
            }
            let mut entry = BTreeMap::from([
                (
                    "backend_available".to_owned(),
                    Node::Bool(health.backend_available),
                ),
                (
                    "embedding_model".to_owned(),
                    Node::Str(health.embedding_model.clone()),
                ),
                (
                    "pending_chunks".to_owned(),
                    Node::Int(health.pending_chunk_count),
                ),
                (
                    "document_count".to_owned(),
                    Node::Int(health.document_count),
                ),
                ("chunk_count".to_owned(), Node::Int(health.chunk_count)),
                (
                    "index_location".to_owned(),
                    Node::Str(health.location.clone()),
                ),
            ]);
            if health.pending_chunk_count > 0 {
                entry.insert(
                    "detail".to_owned(),
                    Node::Str("index has unembeddable (pending) chunks; run `symdesk index --re-embed` once the embedding backend is available".to_owned()),
                );
            }
            if root.is_some()
                && let Ok(mut lifecycle) = open_for_vault(&sidecar_root)
            {
                let statuses = lifecycle.list_index_statuses().unwrap_or_default();
                let count = |state: &str| {
                    i64::try_from(
                        statuses
                            .iter()
                            .filter(|status| status.state == state.as_bytes())
                            .count(),
                    )
                    .unwrap_or(i64::MAX)
                };
                let failed = count("failed");
                entry.insert("failed_documents".to_owned(), Node::Int(failed));
                entry.insert(
                    "unsupported_documents".to_owned(),
                    Node::Int(count("unsupported")),
                );
                entry.insert(
                    "encrypted_documents".to_owned(),
                    Node::Int(count("encrypted")),
                );
                if failed > 0 {
                    status = "warn";
                }
                let _ = lifecycle.close();
            }
            entry.insert("status".to_owned(), Node::Str(status.to_owned()));
            results.insert("retrieval".to_owned(), Node::Map(entry));
        }
    }

    // 3c. Per-vault retrieval index path.
    if let Some(root) = &root {
        let located = std::env::current_dir()
            .map_err(|error| error.to_string())
            .and_then(|cwd| {
                symdesk_index::index_location_for_vault(
                    &root.to_string_lossy(),
                    &environment,
                    &cwd,
                    &std::env::temp_dir(),
                )
                .map_err(|error| error.to_string())
            });
        match located {
            Err(error) => {
                results.insert("retrieval_index".to_owned(), status_error(error));
                all_ok = false;
            }
            Ok(path) => {
                results.insert(
                    "retrieval_index".to_owned(),
                    Node::text_map([
                        ("status", "ok".to_owned()),
                        ("path", path.to_string_lossy().into_owned()),
                    ]),
                );
            }
        }
    }

    results.insert(
        "overall".to_owned(),
        Node::Str(if all_ok { "ok" } else { "error" }.to_owned()),
    );

    // 4. Sibling-tool composition.
    let resolver = Resolver::from_environment().unwrap_or_else(|_| Resolver {
        home: String::new(),
        symaira_bin: String::new(),
        path: String::new(),
        cwd: PathBuf::from("."),
        allow_relative_path: false,
    });
    let workspace_tools = inspect_workspace_tools(&resolver);
    let mut tools = BTreeMap::new();
    let mut versions = BTreeMap::new();
    let mut origins = BTreeMap::new();
    for tool in &workspace_tools {
        if tool.found {
            tools.insert(tool.name.clone(), Node::Str("ok".to_owned()));
            versions.insert(tool.name.clone(), Node::Str(tool.version.clone()));
        } else {
            tools.insert(tool.name.clone(), Node::Str("not_found".to_owned()));
            versions.insert(tool.name.clone(), Node::Str(String::new()));
        }
        origins.insert(
            tool.name.clone(),
            Node::Str(legacy_tool_origin(&tool.origin).to_owned()),
        );
    }
    results.insert("tools".to_owned(), Node::Map(tools));
    results.insert("versions".to_owned(), Node::Map(versions));
    results.insert("tool_origins".to_owned(), Node::Map(origins));
    results.insert(
        "workspace_tools".to_owned(),
        Node::Raw {
            json: serde_json::to_string(&workspace_tools).unwrap_or_default(),
            text: format!(
                "[{}]",
                workspace_tools
                    .iter()
                    .map(WorkspaceTool::go_text)
                    .collect::<Vec<_>>()
                    .join(" ")
            ),
        },
    );
    let managed_dir = resolver.managed_runtime_dir();
    let managed_exists = resolver.managed_runtime_dir_exists();
    results.insert(
        "managed_runtime".to_owned(),
        Node::Map(BTreeMap::from([
            ("dir".to_owned(), Node::Str(managed_dir.clone())),
            ("exists".to_owned(), Node::Bool(managed_exists)),
        ])),
    );

    // 5. Sync conflicts.
    let mut conflicts = Vec::new();
    if let Some(root) = &root
        && let Ok(paths) = walk_markdown(root)
    {
        for relative in paths {
            let rel = relative.to_string_lossy().replace('\\', "/");
            let base = relative
                .file_name()
                .map(|name| name.to_string_lossy().into_owned())
                .unwrap_or_default();
            if base.contains(" 2.md") || base.contains("conflicted copy") {
                conflicts.push(rel);
            }
        }
    }
    results.insert(
        "conflicts".to_owned(),
        Node::List(conflicts.iter().cloned().map(Node::Str).collect()),
    );

    // 6. AI provider.
    let provider = if config.llm_provider.is_empty() {
        "ollama".to_owned()
    } else {
        config.llm_provider.clone()
    };
    let mut ai = BTreeMap::from([("provider".to_owned(), provider.clone())]);
    if provider == "anthropic" {
        let tool_cache = symdesk_core::compose_probe::ToolCache::default();
        let secret_environment = secrets::Environment {
            api_key: std::env::var("SYMDESK_LLM_API_KEY")
                .ok()
                .filter(|value| !value.is_empty()),
            resolver: &resolver,
            tools: &tool_cache,
            path: &resolver.path,
        };
        ai.insert(
            "secret_source".to_owned(),
            secrets::source_in(config.api_key_reference(), &secret_environment),
        );
        let model = if config.llm_model.is_empty() {
            DEFAULT_ANTHROPIC_MODEL.to_owned()
        } else {
            config.llm_model.clone()
        };
        ai.insert("model".to_owned(), model);
    }
    results.insert(
        "ai".to_owned(),
        Node::Map(
            ai.iter()
                .map(|(key, value)| (key.clone(), Node::Str(value.clone())))
                .collect(),
        ),
    );

    // 7. Externalized agent results (informational).
    if let Some(root) = &root {
        let sized = crate::mcp::results_root(root).and_then(|path| results_size(&path));
        results.insert(
            "agent_results".to_owned(),
            match sized {
                Err(error) => status_error(error),
                Ok((bytes, files)) => Node::Map(BTreeMap::from([
                    ("status".to_owned(), Node::Str("ok".to_owned())),
                    ("files".to_owned(), Node::Int(files)),
                    ("bytes".to_owned(), Node::Int(bytes)),
                ])),
            },
        );
    }

    if output_json {
        let rendered = Node::Map(results).json();
        let code = write_stdout(format!("{}\n", go_escape_json(rendered)));
        return finish(all_ok, true, code);
    }
    let mut out = String::new();
    for (key, value) in &results {
        if !matches!(
            key.as_str(),
            "overall"
                | "tools"
                | "versions"
                | "tool_origins"
                | "managed_runtime"
                | "conflicts"
                | "asn"
        ) {
            out.push_str(&format!("{key}: {}\n", value.text()));
        }
    }
    if let Some(report) = &asn_report {
        out.push_str(&format!(
            "asn: {} assigned, {} malformed, {} duplicate values\n",
            report.assigned,
            report.malformed.as_ref().map_or(0, Vec::len),
            report.duplicates.as_ref().map_or(0, Vec::len)
        ));
        for item in report.malformed.iter().flatten() {
            out.push_str(&format!("  malformed: {} ({})\n", item.path, item.message));
        }
        for duplicate in report.duplicates.iter().flatten() {
            out.push_str(&format!(
                "  duplicate ASN {}: {}\n",
                duplicate.asn,
                duplicate.paths.join(", ")
            ));
        }
        for item in report.parse_errors.iter().flatten() {
            out.push_str(&format!(
                "  parse error: {} ({})\n",
                item.path, item.message
            ));
        }
    }
    if conflicts.is_empty() {
        out.push_str("conflicts: none\n");
    } else {
        out.push_str(&format!("conflicts: {} found\n", conflicts.len()));
        for conflict in &conflicts {
            out.push_str(&format!("  - {conflict}\n"));
        }
    }
    out.push_str("tools: tools this workspace can use\n");
    let mut missing_any = false;
    for tool in &workspace_tools {
        if tool.found {
            out.push_str(&format!(
                "  {}: ok (version {}, from {})\n",
                tool.name, tool.version, tool.origin
            ));
            if tool.managed_shadows_newer {
                out.push_str(&format!(
                    "    warning: managed version {} shadows newer PATH version {}\n",
                    tool.managed_version, tool.path_version
                ));
            }
        } else {
            out.push_str(&format!(
                "  {}: not found (managed: absent, PATH: absent)\n",
                tool.name
            ));
            missing_any = true;
        }
    }
    out.push_str(&format!("managed runtime: {managed_dir}"));
    if managed_exists {
        out.push_str(" (present)\n");
    } else {
        out.push_str(" (absent)\n");
        if missing_any {
            out.push_str("  hint: run `symbrain setup` to install the managed runtime, or install the missing tools via Homebrew.\n");
        }
    }
    out.push_str(&format!("ai: provider={provider}"));
    if let Some(source) = ai.get("secret_source") {
        out.push_str(&format!(", secret_source={source}"));
    }
    if let Some(model) = ai.get("model") {
        out.push_str(&format!(", model={model}"));
    }
    out.push('\n');
    out.push_str(&format!(
        "Overall status: {}\n",
        if all_ok { "ok" } else { "error" }
    ));
    let code = write_stdout(out);
    finish(all_ok, false, code)
}

fn finish(all_ok: bool, output_json: bool, printed: ExitCode) -> ExitCode {
    if all_ok {
        return printed;
    }
    if output_json {
        // The report already carries "overall":"error"; Go drops the error
        // envelope and keeps the non-zero exit.
        return process_exit(CoreExitCode::Generic);
    }
    write_stderr(
        "doctor: one or more health checks failed\n",
        CoreExitCode::Generic,
    )
}
