//! `symdesk vault health` (Go `cmd/symdesk/vault_health.go`, `internal/health`).
//!
//! A read-only scan: parse errors, missing frontmatter, derived-artifact
//! freshness, broken Markdown links and wikilinks, near-duplicate groups.

use std::{
    fs,
    path::{Path, PathBuf},
    process::ExitCode,
};

use clap::{Arg, ArgMatches, Command};
use serde::Serialize;
use symdesk_vault::{
    HealthLinkResolver, LinkInventory, MarkdownLinkResolver, extract_markdown_links, go_quote,
    normalize_health_link_target, secure_path, walk_all, walk_markdown,
};

use crate::{emit_error, write_go_json, write_stdout};

/// Go `service.DefaultDuplicateThreshold`.
const DEFAULT_THRESHOLD: i64 = 85;

pub fn command() -> Command {
    Command::new("health")
        .arg(Arg::new("extra").num_args(0..).hide(true))
        .arg(
            Arg::new("duplicate-threshold")
                .long("duplicate-threshold")
                .num_args(1)
                .allow_hyphen_values(true)
                .overrides_with("duplicate-threshold"),
        )
}

#[derive(Serialize)]
struct Finding {
    category: &'static str,
    severity: &'static str,
    #[serde(skip_serializing_if = "String::is_empty")]
    path: String,
    message: String,
}

#[derive(Serialize)]
struct RepairAction {
    category: &'static str,
    #[serde(skip_serializing_if = "String::is_empty")]
    path: String,
    action: &'static str,
    reason: &'static str,
}

#[derive(Serialize)]
struct Report {
    vault: String,
    files_scanned: usize,
    findings: Vec<Finding>,
    repair_plan: Vec<RepairAction>,
    healthy: bool,
}

impl Report {
    fn add(
        &mut self,
        (category, severity): (&'static str, &'static str),
        path: &str,
        message: String,
        (action, reason): (&'static str, &'static str),
    ) {
        self.findings.push(Finding {
            category,
            severity,
            path: path.to_owned(),
            message,
        });
        self.repair_plan.push(RepairAction {
            category,
            path: path.to_owned(),
            action,
            reason,
        });
    }
}

pub fn run(command: &ArgMatches, vault: Option<&str>, json_output: bool) -> ExitCode {
    let threshold = match command.get_one::<String>("duplicate-threshold") {
        None => DEFAULT_THRESHOLD,
        Some(raw) => match crate::duplicates_cli::parse_go_int(raw) {
            Ok(value) => value,
            Err(reason) => {
                return emit_error(
                    format!(
                        "invalid argument {} for \"--duplicate-threshold\" flag: strconv.ParseInt: parsing {}: {reason}",
                        go_quote(raw),
                        go_quote(raw)
                    ),
                    json_output,
                );
            }
        },
    };
    if let Some(first) = command
        .get_many::<String>("extra")
        .and_then(|mut values| values.next())
    {
        return emit_error(
            format!(
                "unknown command {} for \"symdesk vault health\"",
                go_quote(first)
            ),
            json_output,
        );
    }
    let (root, mut sidecar) = match crate::relations_cli::service_deps(vault) {
        Ok(deps) => deps,
        Err(error) => return emit_error(error, json_output),
    };
    let outcome = scan(&root, &sidecar, threshold);
    if let Err(error) = sidecar.close() {
        eprintln!("warning: failed to close sidecar database: {error}");
    }
    let report = match outcome {
        Ok(report) => report,
        Err(error) => return emit_error(error, json_output),
    };
    if json_output {
        return write_go_json(&report);
    }
    write_stdout(format!("{}\n", text(&report)))
}

/// Go `fmt` `%+v` of a `health.Report`.
fn text(report: &Report) -> String {
    let findings = report
        .findings
        .iter()
        .map(|finding| {
            format!(
                "{{Category:{} Severity:{} Path:{} Message:{}}}",
                finding.category, finding.severity, finding.path, finding.message
            )
        })
        .collect::<Vec<_>>()
        .join(" ");
    let plan = report
        .repair_plan
        .iter()
        .map(|action| {
            format!(
                "{{Category:{} Path:{} Action:{} Reason:{}}}",
                action.category, action.path, action.action, action.reason
            )
        })
        .collect::<Vec<_>>()
        .join(" ");
    format!(
        "{{Vault:{} FilesScanned:{} Findings:[{findings}] RepairPlan:[{plan}] Healthy:{}}}",
        report.vault, report.files_scanned, report.healthy
    )
}

fn scan(root: &Path, sidecar: &symdesk_index::Sidecar, threshold: i64) -> Result<Report, String> {
    let mut report = Report {
        vault: root.to_string_lossy().into_owned(),
        files_scanned: 0,
        findings: Vec::new(),
        repair_plan: Vec::new(),
        healthy: false,
    };
    let mut docs: Vec<(String, symdesk_vault::Document)> = Vec::new();
    let mut inventory = LinkInventory::default();
    let entries = walk_all(root).map_err(|error| error.to_string())?;
    let canonical = fs::canonicalize(root).map_err(|error| error.to_string())?;
    for entry in entries {
        let rel = entry
            .path
            .to_string_lossy()
            .replace(std::path::MAIN_SEPARATOR, "/");
        let base = rel.rsplit('/').next().unwrap_or(&rel).to_owned();
        let full = canonical.join(&entry.path);
        if go_extension(&base) == ".md" {
            report.files_scanned += 1;
            let doc = match symdesk_render::export_vault::parse_file_in_root(root, &full) {
                Ok(doc) => doc,
                Err(error) => {
                    report.add(
                        ("parse_error", "error"),
                        &rel,
                        error,
                        ("review", "The file could not be parsed safely"),
                    );
                    continue;
                }
            };
            let rel_lower = rel.to_lowercase();
            let base_lower = base.to_lowercase();
            for name in [rel_lower, base_lower] {
                inventory.paths.push(name.clone());
                inventory
                    .paths
                    .push(name.strip_suffix(".md").unwrap_or(&name).to_owned());
            }
            if !doc.title.is_empty() {
                inventory.titles.push(doc.title.trim().to_lowercase());
            }
            for alias in &doc.aliases {
                if !alias.trim().is_empty() {
                    inventory.aliases.push(alias.trim().to_lowercase());
                }
            }
            if doc.frontmatter.is_empty() {
                report.add(
                    ("missing_frontmatter", "warning"),
                    &rel,
                    "Markdown file has no frontmatter".to_owned(),
                    (
                        "review-frontmatter",
                        "Metadata is unavailable to the index and repair tools",
                    ),
                );
            }
            docs.push((rel, doc));
            continue;
        }
        inventory.attachments.push(rel.to_lowercase());
        inventory.attachments.push(base.to_lowercase());
    }
    let wikilinks = HealthLinkResolver::new(&inventory);
    let markdown = MarkdownLinkResolver::new(root).map_err(|error| error.to_string())?;
    for (rel, doc) in &docs {
        check_derived(&mut report, root, &canonical, rel, doc);
        for destination in extract_markdown_links(&doc.body) {
            if markdown.check(&destination) == Some(false) {
                let target = symdesk_vault::markdown_link_target(&destination).unwrap_or_default();
                report.add(
                    ("broken_markdown_link", "warning"),
                    rel,
                    format!(
                        "Markdown target {} does not resolve to a vault file",
                        go_quote(&target)
                    ),
                    (
                        "review-link",
                        "Choose an existing target or remove the stale link",
                    ),
                );
            }
        }
        for link in &doc.links {
            let Some(target) = normalize_health_link_target(link) else {
                continue;
            };
            if wikilinks.exists(&target) {
                continue;
            }
            let extension = go_extension(&target).to_lowercase();
            let message = if !extension.is_empty() && extension != ".md" && extension != ".canvas" {
                format!(
                    "attachment target {} does not resolve to a vault attachment",
                    go_quote(&target)
                )
            } else {
                format!(
                    "wikilink target {} does not resolve to a vault document",
                    go_quote(&target)
                )
            };
            report.add(
                ("broken_wikilink", "warning"),
                rel,
                message,
                (
                    "review-link",
                    "Choose an existing target or remove the stale link",
                ),
            );
        }
    }
    let groups = sidecar
        .similar_all(root, threshold)
        .map_err(|error| error.to_string())?;
    for group in groups.iter().filter(|group| group.members.len() >= 2) {
        for member in &group.members {
            report.add(
                ("near_duplicate", "warning"),
                &member.path,
                format!(
                    "belongs to a near-duplicate group at {}% similarity",
                    member.similarity
                ),
                (
                    "review-duplicate",
                    "Review the group before choosing a canonical document",
                ),
            );
        }
    }
    report
        .findings
        .sort_by(|a, b| a.path.cmp(&b.path).then_with(|| a.category.cmp(b.category)));
    report.healthy = report.findings.is_empty();
    Ok(report)
}

fn check_derived(
    report: &mut Report,
    root: &Path,
    canonical: &Path,
    rel: &str,
    doc: &symdesk_vault::Document,
) {
    if !(doc.derived || !doc.derived_from.is_empty()) || doc.derived_from.is_empty() {
        return;
    }
    let doc_path = canonical.join(rel);
    let Some(source) = resolve_derived_source(root, canonical, &doc_path, &doc.derived_from) else {
        report.add(
            ("orphaned_derived_artifact", "warning"),
            rel,
            format!(
                "derived artifact {} source {} does not exist",
                go_quote(rel),
                go_quote(&doc.derived_from)
            ),
            (
                "review-derived",
                "Source document is missing or has been deleted",
            ),
        );
        return;
    };
    let source_time = fs::metadata(&source).and_then(|meta| meta.modified());
    let derived_time = fs::metadata(&doc_path).and_then(|meta| meta.modified());
    if let (Ok(source_time), Ok(derived_time)) = (source_time, derived_time)
        && source_time > derived_time
    {
        report.add(
            ("stale_derived_artifact", "warning"),
            rel,
            format!(
                "derived artifact {} is older than its source {}",
                go_quote(rel),
                go_quote(&doc.derived_from)
            ),
            (
                "regenerate-derived",
                "Source document was modified after artifact generation",
            ),
        );
    }
}

/// Go `resolveDerivedSource`.
fn resolve_derived_source(
    root: &Path,
    canonical: &Path,
    doc_path: &Path,
    derived_from: &str,
) -> Option<PathBuf> {
    let mut target = derived_from.trim();
    if target.starts_with("[[") && target.ends_with("]]") {
        target = &target[2..target.len() - 2];
        if let Some((before, _)) = target.split_once('|') {
            target = before;
        }
        if let Some(index) = target.find(['#', '^']) {
            target = &target[..index];
        }
    }
    let target = target.trim();
    if target.is_empty() {
        return None;
    }
    let parent = doc_path.parent().unwrap_or(canonical);
    let mut candidates = vec![join_clean(root, target), join_clean(parent, target)];
    if go_extension(target).is_empty() {
        candidates.push(join_clean(root, &format!("{target}.md")));
        candidates.push(join_clean(parent, &format!("{target}.md")));
    }
    for candidate in candidates {
        let relative = relative_to(root, &candidate)
            .or_else(|| relative_to(canonical, &candidate))
            .filter(|relative| !relative.starts_with(".."));
        let Some(relative) = relative else { continue };
        if let Ok(resolved) = secure_path(root, &relative)
            && fs::metadata(&resolved).is_ok_and(|meta| !meta.is_dir())
        {
            return Some(resolved);
        }
    }
    for relative in walk_markdown(root).ok()? {
        let name = relative
            .file_name()
            .map(|name| name.to_string_lossy().into_owned())
            .unwrap_or_default();
        let stem = name.strip_suffix(&go_extension(&name)).unwrap_or(&name);
        let full = canonical.join(&relative);
        if name.to_lowercase() == target.to_lowercase()
            || stem.to_lowercase() == target.to_lowercase()
            || full.to_string_lossy().to_lowercase() == target.to_lowercase()
        {
            return Some(full);
        }
    }
    None
}

/// Go `filepath.Join` + `Clean` for a base directory and a relative target.
fn join_clean(base: &Path, target: &str) -> PathBuf {
    let mut parts: Vec<String> = Vec::new();
    let joined = format!("{}/{}", base.to_string_lossy(), target);
    let absolute = joined.starts_with('/');
    for part in joined.split('/') {
        match part {
            "" | "." => {}
            ".." => {
                if parts.last().is_some_and(|last| last != "..") {
                    parts.pop();
                } else if !absolute {
                    parts.push("..".to_owned());
                }
            }
            value => parts.push(value.to_owned()),
        }
    }
    PathBuf::from(format!(
        "{}{}",
        if absolute { "/" } else { "" },
        parts.join("/")
    ))
}

/// Go `filepath.Rel` for a candidate below `base`; escapes report `..`.
fn relative_to(base: &Path, candidate: &Path) -> Option<String> {
    if let Ok(relative) = candidate.strip_prefix(base) {
        return Some(relative.to_string_lossy().into_owned());
    }
    Some("..".to_owned())
}

/// Go `filepath.Ext`: the suffix starting at the final dot of the last element.
fn go_extension(name: &str) -> String {
    let base = name.rsplit('/').next().unwrap_or(name);
    base.rfind('.')
        .map(|index| base[index..].to_owned())
        .unwrap_or_default()
}
