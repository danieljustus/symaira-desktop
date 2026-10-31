//! Classification rules as the Go pipeline and facade apply them:
//! case-insensitive substring match with Go's per-rune simple lowercasing,
//! first-wins scalars, de-duplicated tags and preset precedence
//! (`pipeline.go` classification block, `api.go` `TestRules`/`DryRunRule`).
use crate::{ClassificationRule, Document, Result, Store, validate_classification_rule};
use serde::Serialize;
use std::path::Path;
use symdesk_vault::go_lowercase;

/// Preset metadata that overrides rule results (`IngestOptions.Preset*`).
#[derive(Clone, Debug, Default)]
pub struct Presets<'a> {
    pub category: &'a str,
    pub tags: &'a [String],
    pub correspondent: &'a str,
    pub document_type: &'a str,
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize)]
pub struct Classification {
    pub category: String,
    /// `None` is Go's nil slice (JSON `null`).
    pub tags: Option<Vec<String>>,
    pub correspondent: String,
    pub document_type: String,
}

/// The pipeline classification: every rule whose lowercased pattern is contained
/// in the lowercased text applies in rule order; presets win afterwards.
pub fn classify(rules: &[ClassificationRule], text: &str, presets: &Presets<'_>) -> Classification {
    let lower = go_lowercase(text);
    let mut out = Classification::default();
    let mut tags: Vec<String> = Vec::new();
    for rule in rules {
        if !lower.contains(&go_lowercase(&rule.pattern)) {
            continue;
        }
        match rule.kind.as_str() {
            "category" if out.category.is_empty() => out.category = rule.value.clone(),
            "tag" if !tags.contains(&rule.value) => tags.push(rule.value.clone()),
            "correspondent" if out.correspondent.is_empty() => {
                out.correspondent = rule.value.clone()
            }
            "document_type" if out.document_type.is_empty() => {
                out.document_type = rule.value.clone()
            }
            _ => {}
        }
    }
    out.tags = (!tags.is_empty()).then_some(tags);
    if !presets.category.is_empty() {
        out.category = presets.category.into();
    }
    if !presets.tags.is_empty() {
        out.tags = Some(presets.tags.to_vec());
    }
    if !presets.correspondent.is_empty() {
        out.correspondent = presets.correspondent.into();
    }
    if !presets.document_type.is_empty() {
        out.document_type = presets.document_type.into();
    }
    out
}

/// `RuleMatch` of the facade `TestRules`: no `created_at`.
#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
pub struct RuleMatch {
    pub id: i64,
    pub pattern: String,
    pub kind: String,
    pub value: String,
}

/// Which stored rules would match `text` (the same semantics as [`classify`]).
pub fn test_rules(rules: &[ClassificationRule], text: &str) -> Vec<RuleMatch> {
    let lower = go_lowercase(text);
    rules
        .iter()
        .filter(|r| lower.contains(&go_lowercase(&r.pattern)))
        .map(|r| RuleMatch {
            id: r.id,
            pattern: r.pattern.clone(),
            kind: r.kind.clone(),
            value: r.value.clone(),
        })
        .collect()
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
pub struct ProposedRule {
    pub pattern: String,
    pub kind: String,
    pub value: String,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
pub struct DryRunMatch {
    pub document_id: i64,
    pub note_path: String,
    pub title: String,
    pub matched_rule_ids: Vec<i64>,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
pub struct DryRunSkipped {
    pub document_id: i64,
    pub note_path: String,
    pub reason: String,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
pub struct DryRunResult {
    pub proposed_rule: ProposedRule,
    pub vault_path: String,
    pub total_documents: usize,
    pub matched_documents: usize,
    pub skipped_documents: usize,
    pub matches: Vec<DryRunMatch>,
    pub skipped: Vec<DryRunSkipped>,
}

/// Strips the `---\n<yaml>\n---\n\n` frontmatter the note writer emits.
pub fn note_body(content: &str) -> &str {
    const DELIM: &str = "---\n";
    let Some(rest) = content.strip_prefix(DELIM) else {
        return content;
    };
    match rest.find("\n---\n") {
        None => content,
        Some(i) => {
            let body = &rest[i + "\n---\n".len()..];
            body.strip_prefix('\n').unwrap_or(body)
        }
    }
}

/// Go `os.ReadFile` error text for the failure modes a dry run reports.
fn go_read_error(path: &str, e: &std::io::Error) -> String {
    use std::io::ErrorKind::*;
    let reason = match e.kind() {
        NotFound => return format!("open {path}: no such file or directory"),
        PermissionDenied => return format!("open {path}: permission denied"),
        _ => match e.raw_os_error() {
            Some(21) => return format!("read {path}: is a directory"),
            Some(20) => return format!("open {path}: not a directory"),
            _ => e.to_string(),
        },
    };
    format!("open {path}: {reason}")
}

/// `DryRunRule`: validates the proposed rule, then reports which already-ingested
/// notes would match it, without persisting anything.
pub fn dry_run_rule(
    store: &Store,
    vault: &str,
    pattern: &str,
    kind: &str,
    value: &str,
) -> Result<DryRunResult> {
    validate_classification_rule(pattern, kind, value)?;
    let docs: Vec<Document> = store
        .list_documents()
        .map_err(|e| format!("list documents: {e}"))?
        .unwrap_or_default();
    let mut result = DryRunResult {
        proposed_rule: ProposedRule {
            pattern: pattern.into(),
            kind: kind.into(),
            value: value.into(),
        },
        vault_path: vault.into(),
        total_documents: docs.len(),
        matched_documents: 0,
        skipped_documents: 0,
        matches: Vec::new(),
        skipped: Vec::new(),
    };
    let needle = go_lowercase(pattern);
    for doc in &docs {
        let note_path = doc.vault_path.clone().unwrap_or_default();
        if note_path.is_empty() {
            result.skipped.push(DryRunSkipped {
                document_id: doc.id,
                note_path: String::new(),
                reason: "no note path recorded".into(),
            });
            continue;
        }
        let data = match std::fs::read(&note_path) {
            Ok(d) => d,
            Err(e) => {
                result.skipped.push(DryRunSkipped {
                    document_id: doc.id,
                    reason: go_read_error(&note_path, &e),
                    note_path,
                });
                continue;
            }
        };
        let content = String::from_utf8_lossy(&data);
        if !go_lowercase(note_body(&content)).contains(&needle) {
            continue;
        }
        // filepath.Base then TrimSuffix(filepath.Ext): a leading-dot name keeps no title.
        let base = Path::new(&note_path)
            .file_name()
            .map(|s| s.to_string_lossy().into_owned())
            .unwrap_or_default();
        let title = match base.rfind('.') {
            Some(i) => base[..i].to_owned(),
            None => base,
        };
        result.matches.push(DryRunMatch {
            document_id: doc.id,
            note_path,
            title,
            matched_rule_ids: vec![0],
        });
    }
    result.matched_documents = result.matches.len();
    result.skipped_documents = result.skipped.len();
    Ok(result)
}
