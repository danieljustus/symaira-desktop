//! Notion Markdown + CSV export importer. Markdown remains authoritative:
//! idempotency searches note frontmatter, including notes moved by the user.
//! No SQLite or network access is involved.
use crate::Result;
use regex::Regex;
use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};
use std::{
    collections::BTreeMap,
    fs,
    path::{Path, PathBuf},
    sync::LazyLock,
};
use symdesk_vault::{render_go_yaml_map, render_go_yaml_string, write_atomic};
mod csv;

static ID: LazyLock<Regex> = LazyLock::new(|| {
    Regex::new(r"[\t\n\x0C\r ]*<?([0-9a-f]{32})>?(?:\.md)?[\t\n\x0C\r ]*$").unwrap()
});
static LINKS: LazyLock<Regex> =
    LazyLock::new(|| Regex::new(r"\[\[([^\]]+)\]\]|\[([^\]]+)\]\(([^)]+\.md)\)").unwrap());
static ASSETS: LazyLock<Regex> =
    LazyLock::new(|| Regex::new(r"(!?\[[^\]]*\]\()assets/([^)]+)\)").unwrap());

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct ExportEntry {
    pub source_path: String,
    pub display_name: String,
    pub notion_id: String,
    pub kind: String,
    pub mime: String,
    pub category: String,
    pub assets_root: String,
    pub rel_path: String,
}
#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct PageResult {
    pub source_path: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub vault_path: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub sha256: String,
    pub status: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub reason: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub error: String,
}
#[derive(Clone, Debug, Default)]
pub struct Options {
    pub source_dir: String,
    pub vault: String,
    pub dry_run: bool,
    pub import_run_id: String,
}
#[derive(Clone, Debug, Default)]
pub struct Stats {
    pub imported: usize,
    pub skipped: usize,
    pub failed: usize,
    pub total: usize,
    pub warnings: Vec<String>,
    pub run_id: String,
    pub results: Vec<PageResult>,
}
fn text(path: impl AsRef<Path>) -> String {
    path.as_ref().to_string_lossy().into_owned()
}
fn hash(data: &[u8]) -> String {
    format!("{:x}", Sha256::digest(data))
}

pub fn sanitize_base_name(name: &str) -> String {
    let value: String = name
        .chars()
        .map(|c| if "/\\:*?\"<>|".contains(c) { '_' } else { c })
        .collect();
    let value = value.trim();
    if value.is_empty() {
        "untitled".into()
    } else {
        value.into()
    }
}
fn split_name(base: &str) -> (String, String) {
    if let Some(c) = ID.captures(base) {
        (base[..c.get(0).unwrap().start()].into(), c[1].into())
    } else {
        (base.into(), String::new())
    }
}
pub fn parse_entry(path: &Path, rel: &str, root: &Path, csv: bool) -> ExportEntry {
    let filename = path.file_name().unwrap_or_default().to_string_lossy();
    // filepath.Ext treats dotfiles as extensions (Rust file_stem does not).
    let base = filename
        .rfind('.')
        .map_or(filename.as_ref(), |i| &filename[..i]);
    let (display_name, notion_id) = split_name(base);
    let parent = path.parent().and_then(Path::file_name);
    let category = if csv {
        "database".into()
    } else if parent != root.file_name() {
        parent
            .map(|p| p.to_string_lossy().into_owned())
            .unwrap_or_default()
    } else {
        String::new()
    };
    let assets = root.join("assets");
    ExportEntry {
        source_path: text(path),
        display_name,
        notion_id,
        kind: if csv { "csv" } else { "page" }.into(),
        mime: if csv { "text/csv" } else { "text/markdown" }.into(),
        category,
        assets_root: if !csv && assets.is_dir() {
            text(assets)
        } else {
            String::new()
        },
        rel_path: rel.into(),
    }
}
fn read_dir(dir: &Path) -> Result<Vec<fs::DirEntry>> {
    let mut entries = fs::read_dir(dir)
        .map_err(|e| e.to_string())?
        .collect::<std::io::Result<Vec<_>>>()
        .map_err(|e| e.to_string())?;
    entries.sort_by_key(|e| e.file_name());
    Ok(entries)
}
pub fn resolve_export_dir(dir: &Path) -> Result<PathBuf> {
    let meta = fs::metadata(dir)
        .map_err(|e| format!("stat export dir: stat {}: {}", text(dir), io_message(&e)))?;
    if !meta.is_dir() {
        return Err(format!("export path is not a directory: {}", text(dir)));
    }
    let entries = read_dir(dir).map_err(|e| format!("read export dir: {e}"))?;
    let mut dirs = Vec::new();
    let mut markdown = false;
    for e in entries {
        let name = e.file_name().to_string_lossy().into_owned();
        if e.file_type().map_err(|e| e.to_string())?.is_dir() && !name.starts_with('.') {
            dirs.push(e.path());
        } else if name.to_ascii_lowercase().ends_with(".md") {
            markdown = true;
        }
    }
    Ok(if dirs.len() == 1 && !markdown {
        dirs.remove(0)
    } else {
        dir.to_path_buf()
    })
}
pub fn discover_export(dir: &Path) -> Result<Vec<ExportEntry>> {
    let root = resolve_export_dir(dir)?;
    fn walk(root: &Path, dir: &Path, out: &mut Vec<ExportEntry>) -> Result<()> {
        for e in read_dir(dir)? {
            let name = e.file_name().to_string_lossy().into_owned();
            let path = e.path();
            if e.file_type().map_err(|e| e.to_string())?.is_dir() {
                if name.starts_with('.') || (dir == root && name == "assets") {
                    continue;
                }
                walk(root, &path, out)?;
            } else {
                let ext = path
                    .extension()
                    .unwrap_or_default()
                    .to_string_lossy()
                    .to_ascii_lowercase();
                if ext == "md" || ext == "csv" {
                    out.push(parse_entry(
                        &path,
                        &text(path.strip_prefix(root).unwrap()),
                        root,
                        ext == "csv",
                    ));
                }
            }
        }
        Ok(())
    }
    let mut out = Vec::new();
    walk(&root, &root, &mut out).map_err(|e| format!("walk export: {e}"))?;
    Ok(out)
}
pub fn compute_vault_path(vault: &str, entry: &ExportEntry) -> String {
    if vault.is_empty() {
        return String::new();
    }
    let mut path = PathBuf::from(vault);
    if let Some(parent) = Path::new(&entry.rel_path).parent() {
        for part in parent.components() {
            let part = sanitize_base_name(&text(part.as_os_str()));
            if part != "." && part != ".." {
                path.push(part);
            }
        }
    }
    path.push(format!("{}.md", sanitize_base_name(&entry.display_name)));
    text(path)
}
pub fn rewrite_links(body: &str, names: &BTreeMap<String, String>) -> String {
    LINKS
        .replace_all(body, |c: &regex::Captures<'_>| {
            if let Some(inner) = c.get(1) {
                let s = inner.as_str();
                if let Some(id) = ID.captures(s) {
                    return format!(
                        "[[{}]]",
                        names
                            .get(&id[1])
                            .map(String::as_str)
                            .unwrap_or_else(|| s[..id.get(0).unwrap().start()].trim())
                    );
                }
                return format!("[[{}]]", s.trim());
            }
            let display = &c[2];
            let file = &c[3];
            if let Some(id) = ID.captures(file) {
                return format!(
                    "[[{}]]",
                    names.get(&id[1]).map(String::as_str).unwrap_or(display)
                );
            }
            let decoded = file.replace("%20", " ");
            let decoded = decoded.strip_suffix(".md").unwrap_or(&decoded);
            if let Some(id) = ID.captures(decoded)
                && let Some(name) = names.get(&id[1])
            {
                return format!("[[{name}]]");
            }
            format!("[[{display}]]")
        })
        .into_owned()
}
fn io_message(e: &std::io::Error) -> String {
    match e.kind() {
        std::io::ErrorKind::NotFound => "no such file or directory".into(),
        std::io::ErrorKind::PermissionDenied => "permission denied".into(),
        _ => e.to_string(),
    }
}
fn read_source(path: &str, context: &str) -> Result<Vec<u8>> {
    fs::read(path).map_err(|e| format!("{context}: open {path}: {}", io_message(&e)))
}
fn parse_meta(data: &str) -> Option<noyalib::Value> {
    let rest = data.strip_prefix("---\n")?;
    let end = rest.find("\n---")?;
    noyalib::from_str(&rest[..end]).ok()
}
fn has_run(path: &Path, run: &str, source: Option<&str>) -> bool {
    let Ok(data) = fs::read_to_string(path) else {
        return false;
    };
    let Some(meta) = parse_meta(&data) else {
        return false;
    };
    let Some(map) = meta.as_mapping() else {
        return false;
    };
    map.get("import_run_id").and_then(noyalib::Value::as_str) == Some(run)
        && source.is_none_or(|s| map.get("source_path").and_then(noyalib::Value::as_str) == Some(s))
}
fn existing(vault: &Path, run: &str, source: &str) -> Option<String> {
    for e in read_dir(vault).ok()? {
        let path = e.path();
        if e.file_type().ok()?.is_dir() {
            if let Some(found) = existing(&path, run, source) {
                return Some(found);
            }
        } else if text(&path).ends_with(".md") && has_run(&path, run, Some(source)) {
            return Some(text(path));
        }
    }
    None
}
fn copy_assets(root: &str, vault: &str, body: &str) -> Result<String> {
    let dst = Path::new(vault).join("assets");
    if !Path::new(root).exists() {
        return Ok(body.into());
    }
    private_dirs(&dst)?;
    let mut seen = BTreeMap::new();
    for c in ASSETS.captures_iter(body) {
        let name = &c[2];
        if seen.contains_key(name) {
            continue;
        }
        // Lexically bounded like the Go oracle. Symlink confinement is not
        // claimed by this compatibility layer; use trusted extracted exports.
        if Path::new(name).components().any(|p| {
            matches!(
                p,
                std::path::Component::ParentDir
                    | std::path::Component::RootDir
                    | std::path::Component::Prefix(_)
            )
        }) {
            continue;
        }
        let Ok(data) = fs::read(Path::new(root).join(name)) else {
            continue;
        };
        let ext = Path::new(name)
            .extension()
            .map(|e| format!(".{}", e.to_string_lossy()))
            .unwrap_or_default();
        let target = format!("{}{ext}", hash(&data));
        fs::write(dst.join(&target), data).map_err(|e| format!("copy asset {name}: {e}"))?;
        seen.insert(name.to_owned(), target);
    }
    let mut out = body.to_owned();
    for (name, target) in seen {
        out = out.replace(&format!("assets/{name}"), &format!("assets/{target}"));
    }
    Ok(out)
}
fn private_dirs(path: &Path) -> Result<()> {
    let mut builder = fs::DirBuilder::new();
    builder.recursive(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::DirBuilderExt;
        builder.mode(0o700);
    }
    builder
        .create(path)
        .map_err(|e| format!("create vault directory: {e}"))
}
fn write_note(vault: &str, path: &str, frontmatter: &str, body: &str) -> Result<()> {
    // Generated paths are confined; reject all traversal before opening.
    if vault.is_empty()
        || !Path::new(path).starts_with(vault)
        || Path::new(path)
            .components()
            .any(|p| matches!(p, std::path::Component::ParentDir))
    {
        return Err(format!("path escapes target directory: {path}"));
    }
    private_dirs(Path::new(path).parent().unwrap_or(Path::new(".")))?;
    let tail = if body.ends_with('\n') { "" } else { "\n" };
    write_atomic(
        Path::new(path),
        format!("---\n{frontmatter}---\n\n{body}{tail}").as_bytes(),
    )
    .map_err(|e| e.to_string())
}
fn scalar(value: &str) -> Result<String> {
    render_go_yaml_string(value, 4, false).map_err(|e| e.to_string())
}
fn note_yaml(
    entry: &ExportEntry,
    run: &str,
    digest: &str,
    now: &str,
    tags: &[&str],
) -> Result<String> {
    let mut out = format!(
        "source_path: {}\nimported_from: notion\n",
        scalar(&entry.source_path)?
    );
    if !run.is_empty() {
        out.push_str(&format!("import_run_id: {}\n", scalar(run)?));
    }
    out.push_str(&format!(
        "ingested_at: {now}\nsha256: {digest}\nmime: {}\ntags:\n",
        entry.mime
    ));
    for tag in tags {
        out.push_str(&format!("    - {tag}\n"));
    }
    out.push_str(&format!(
        "category: {}\narchive_path: \"\"\n",
        scalar(&entry.category)?
    ));
    Ok(out)
}
/// Explicit clock seam for deterministic replay; normal callers use [`run`].
pub fn run_at(opts: &Options, now: &str, generated_run_id: &str) -> Result<Stats> {
    let run = if opts.import_run_id.is_empty() {
        generated_run_id
    } else {
        &opts.import_run_id
    };
    let export = resolve_export_dir(Path::new(&opts.source_dir))?;
    // Go intentionally resolves twice (Run, then discoverExport).
    let entries = discover_export(&export).map_err(|e| format!("discover export: {e}"))?;
    let names = entries
        .iter()
        .filter(|e| !e.notion_id.is_empty())
        .map(|e| (e.notion_id.clone(), e.display_name.clone()))
        .collect();
    let mut stats = Stats {
        run_id: run.into(),
        ..Stats::default()
    };
    for entry in entries {
        let results = if entry.kind == "csv" {
            convert_csv(&entry, &opts.vault, run, opts.dry_run, now)
        } else {
            vec![process_page(&entry, opts, run, now, &names)]
        };
        for r in results {
            match r.status.as_str() {
                "imported" | "would-import" => stats.imported += 1,
                "skipped" => stats.skipped += 1,
                _ => {
                    stats.failed += 1;
                    if !r.error.is_empty() {
                        stats
                            .warnings
                            .push(format!("{}: {}", entry.source_path, r.error));
                    }
                }
            }
            stats.results.push(r);
        }
    }
    stats.total = stats.results.len();
    Ok(stats)
}
pub fn run(opts: &Options) -> Result<Stats> {
    let now = time::OffsetDateTime::now_utc();
    let stamp = now
        .format(&time::format_description::well_known::Rfc3339)
        .map_err(|e| e.to_string())?;
    let run_id = format!(
        "notion-{:04}{:02}{:02}T{:02}{:02}{:02}.{:09}Z",
        now.year(),
        u8::from(now.month()),
        now.day(),
        now.hour(),
        now.minute(),
        now.second(),
        now.nanosecond()
    );
    run_at(opts, &stamp, &run_id)
}
fn process_page(
    entry: &ExportEntry,
    opts: &Options,
    run: &str,
    now: &str,
    names: &BTreeMap<String, String>,
) -> PageResult {
    let mut r = PageResult {
        source_path: entry.source_path.clone(),
        status: "failed".into(),
        ..PageResult::default()
    };
    if opts.dry_run {
        r.status = "would-import".into();
        return r;
    }
    let data = match read_source(&entry.source_path, "read source") {
        Ok(d) => d,
        Err(e) => {
            r.error = e;
            return r;
        }
    };
    let digest = hash(&data);
    if !opts.vault.is_empty()
        && !run.is_empty()
        && let Some(path) = existing(Path::new(&opts.vault), run, &entry.source_path)
    {
        r.status = "skipped".into();
        r.reason = "already imported in a previous run".into();
        r.vault_path = path;
        r.sha256 = digest;
        return r;
    }
    let mut body = String::from_utf8_lossy(&data).into_owned();
    if !entry.assets_root.is_empty() {
        body = copy_assets(&entry.assets_root, &opts.vault, &body).unwrap_or_default();
    }
    body = rewrite_links(&body, names);
    let path = compute_vault_path(&opts.vault, entry);
    let result = note_yaml(entry, run, &digest, now, &["notion"])
        .and_then(|fm| write_note(&opts.vault, &path, &fm, &body));
    if let Err(e) = result {
        r.error = format!("write note: {e}");
        return r;
    }
    r.status = "imported".into();
    r.vault_path = path;
    r.sha256 = digest;
    r
}

pub fn csv_row_body(props: &BTreeMap<String, String>) -> String {
    props
        .iter()
        .filter(|(_, v)| !v.is_empty())
        .map(|(k, v)| format!("**{k}:** {v}\n\n"))
        .collect()
}
pub fn csv_index_body(entry: &ExportEntry, headers: &[String], rows: usize) -> String {
    let mut out = format!(
        "# {}\n\nDatabase with {rows} rows and {} columns.\n\n## Columns\n\n",
        entry.display_name,
        headers.len()
    );
    for h in headers {
        out.push_str(&format!("- {h}\n"));
    }
    out.push('\n');
    out
}
pub fn convert_csv(
    entry: &ExportEntry,
    vault: &str,
    run: &str,
    dry_run: bool,
    now: &str,
) -> Vec<PageResult> {
    let failed = |error: String| PageResult {
        source_path: entry.source_path.clone(),
        status: "failed".into(),
        error,
        ..PageResult::default()
    };
    let data = match read_source(&entry.source_path, "open CSV") {
        Ok(d) => d,
        Err(e) => return vec![failed(e)],
    };
    let records = match csv::read_all(&String::from_utf8_lossy(&data)) {
        Ok(r) => r,
        Err(e) => return vec![failed(format!("read CSV: {e}"))],
    };
    if records.len() < 2 {
        return vec![PageResult {
            source_path: entry.source_path.clone(),
            status: "skipped".into(),
            reason: "empty CSV (no data rows)".into(),
            ..PageResult::default()
        }];
    }
    let headers = &records[0];
    let dir = Path::new(vault)
        .join("databases")
        .join(sanitize_base_name(&entry.display_name));
    let mut out = Vec::new();
    for (i, row) in records[1..].iter().enumerate() {
        let props: BTreeMap<_, _> = headers.iter().cloned().zip(row.iter().cloned()).collect();
        let title = if row[0].is_empty() {
            format!("Row {}", i + 1)
        } else {
            row[0].clone()
        };
        let path = text(dir.join(format!("{}.md", sanitize_base_name(&title))));
        let digest = hash(row.join("\x1f").as_bytes());
        let result = csv_result(entry, vault, run, dry_run, &path, &digest, || {
            let mut fm = BTreeMap::new();
            for (k, v) in [
                ("source_path", entry.source_path.as_str()),
                ("imported_from", "notion"),
                ("import_run_id", run),
                ("sha256", digest.as_str()),
                ("mime", "text/csv"),
                ("category", entry.category.as_str()),
            ] {
                fm.insert(k.into(), noyalib::Value::String(v.into()));
            }
            // A timestamp tag keeps yaml.v3's unquoted time.Time representation.
            let stamp: noyalib::Value = noyalib::from_str(now).map_err(|e| e.to_string())?;
            fm.insert("ingested_at".into(), stamp);
            fm.insert(
                "tags".into(),
                noyalib::Value::Sequence(
                    ["notion", "csv-row", "database"]
                        .into_iter()
                        .map(|s| noyalib::Value::String(s.into()))
                        .collect(),
                ),
            );
            for (k, v) in &props {
                let key = if [
                    "source_path",
                    "imported_from",
                    "import_run_id",
                    "ingested_at",
                    "mime",
                    "tags",
                    "category",
                    "sha256",
                    "archive_path",
                    "sidecar_path",
                    "ocr_engine",
                ]
                .contains(&k.to_lowercase().as_str())
                {
                    format!("csv_{k}")
                } else {
                    k.clone()
                };
                fm.insert(key, noyalib::Value::String(v.clone()));
            }
            // noyalib resolves timestamps as strings; this field is a Go
            // time.Time, not a user string. Preserve its scalar type on output.
            let yaml = render_go_yaml_map(&fm).map_err(|e| e.to_string())?;
            let yaml = yaml
                .lines()
                .map(|line| {
                    if line.starts_with("ingested_at: ") {
                        format!("ingested_at: {now}\n")
                    } else {
                        format!("{line}\n")
                    }
                })
                .collect::<String>();
            write_note(vault, &path, &yaml, &csv_row_body(&props))
        });
        out.push(result.unwrap_or_else(|e| failed(format!("write row {}: {e}", i + 1))));
    }
    let path = text(dir.join("_index.md"));
    let digest = hash(&data);
    let result = csv_result(entry, vault, run, dry_run, &path, &digest, || {
        let fm = note_yaml(
            entry,
            run,
            &digest,
            now,
            &["notion", "csv-index", "database"],
        )?;
        write_note(
            vault,
            &path,
            &fm,
            &csv_index_body(entry, headers, records.len() - 1),
        )
    });
    out.push(result.unwrap_or_else(|e| failed(format!("write index: {e}"))));
    out
}
fn csv_result(
    entry: &ExportEntry,
    vault: &str,
    run: &str,
    dry_run: bool,
    path: &str,
    digest: &str,
    write: impl FnOnce() -> Result<()>,
) -> Result<PageResult> {
    let mut r = PageResult {
        source_path: entry.source_path.clone(),
        vault_path: path.into(),
        ..PageResult::default()
    };
    if !dry_run && !vault.is_empty() && !run.is_empty() && has_run(Path::new(path), run, None) {
        r.status = "skipped".into();
        r.reason = "already imported in a previous run".into();
        return Ok(r);
    }
    if !dry_run {
        write()?;
    }
    r.status = if dry_run { "would-import" } else { "imported" }.into();
    r.sha256 = digest.into();
    Ok(r)
}
