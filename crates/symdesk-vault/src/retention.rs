#![deny(unsafe_code)]

//! Retention rules, evaluation and staged proposals. Port of `internal/retention`.
//!
//! The decision logic and the state files are compared byte for byte against Go
//! vectors (`testdata/port/vault/retention.json`, written by
//! `internal/retention/port_retention_contract_test.go`).
//!
//! ponytail: two entry points are not ported yet and are deliberately absent
//! rather than half-done — `LoadRules`, whose YAML multi-document reader needs a
//! Rust YAML parser and belongs to the CLI slice, and `DocMetaFromDocument`,
//! which needs the vault document model and belongs to the document slice.

use std::path::{Path, PathBuf};

use serde::{Deserialize, Serialize};
use time::format_description::well_known::Rfc3339;
use time::{Duration, OffsetDateTime};

pub const ACTION_TRASH: &str = "trash";
pub const ACTION_FLAG_REVIEW: &str = "flag_review";

pub const PROPOSAL_STATUS_PENDING: &str = "pending";
pub const PROPOSAL_STATUS_ACCEPTED: &str = "accepted";
pub const PROPOSAL_STATUS_REJECTED: &str = "rejected";
pub const PROPOSAL_STATUS_FAILED: &str = "failed";
pub const PROPOSAL_STATUS_PARTIAL: &str = "partial";

pub const PROPOSAL_ITEM_STATUS_ACCEPTED: &str = "accepted";
pub const PROPOSAL_ITEM_STATUS_ACTION_COMPLETED: &str = "action_completed";

/// Go: `retention.RawSource`. Paths and bytes are both authoritative inputs.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct RawSource {
    pub path: String,
    pub data: Vec<u8>,
}

/// Go: `retention.Fingerprint`. The version marker and every value are encoded
/// with decimal byte length, a colon, the bytes, and a trailing newline.
#[must_use]
pub fn fingerprint(handle: Option<&[u8]>, sources: &[RawSource]) -> String {
    let mut encoded = Vec::new();
    encoded.extend_from_slice(b"symdesk-retention-fingerprint-v1\0");
    append_fingerprint_part(&mut encoded, handle.unwrap_or_default());
    for source in sources {
        append_fingerprint_part(&mut encoded, source.path.as_bytes());
        append_fingerprint_part(&mut encoded, &source.data);
    }
    crate::sha256::digest(&encoded)
        .iter()
        .map(|byte| format!("{byte:02x}"))
        .collect()
}

fn append_fingerprint_part(encoded: &mut Vec<u8>, value: &[u8]) {
    encoded.extend_from_slice(value.len().to_string().as_bytes());
    encoded.push(b':');
    encoded.extend_from_slice(value);
    encoded.push(b'\n');
}

/// Go: `retention.Rule`. `period` is the Go duration in nanoseconds and is kept
/// as a field so the wire format matches. Every field defaults when the YAML
/// document omits it, because Go decodes into a zero-valued struct and only
/// `Validate` decides whether that is acceptable.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct Rule {
    #[serde(default)]
    pub name: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub description: Option<String>,
    #[serde(default)]
    pub selector: Selector,
    #[serde(default)]
    pub period: i64,
    #[serde(default)]
    pub period_days: i64,
    #[serde(default)]
    pub reference_field: String,
    #[serde(default)]
    pub action: String,
}

/// Go: `retention.Selector`. Every non-empty field is ANDed; an empty field
/// matches everything on its dimension.
#[derive(Debug, Clone, Default, Serialize, Deserialize, PartialEq, Eq)]
pub struct Selector {
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub document_type: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub status: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub category: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub person: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub correspondent: String,
    #[serde(
        default,
        deserialize_with = "de_vec_or_null",
        skip_serializing_if = "Vec::is_empty"
    )]
    pub tags: Vec<String>,
}

/// Go: `retention.DocMeta`. Serialized with Go's field names: the Go struct
/// carries no JSON tags, so the wire format is PascalCase.
#[derive(Debug, Clone, Default, Serialize, Deserialize, PartialEq, Eq)]
pub struct DocMeta {
    #[serde(rename = "Path")]
    pub path: String,
    #[serde(rename = "Title")]
    pub title: String,
    #[serde(rename = "DocumentDate")]
    pub document_date: String,
    #[serde(rename = "Created")]
    pub created: String,
    #[serde(rename = "DueDate")]
    pub due_date: String,
    #[serde(rename = "Status")]
    pub status: String,
    #[serde(rename = "Correspondent")]
    pub correspondent: String,
    #[serde(rename = "DocumentType")]
    pub document_type: String,
    #[serde(rename = "Person")]
    pub person: String,
    #[serde(rename = "Tags", default, deserialize_with = "de_vec_or_null")]
    pub tags: Vec<String>,
}

/// Go encodes a nil slice as `null`; the port reads it back as empty.
fn de_vec_or_null<'de, D, T>(deserializer: D) -> Result<Vec<T>, D::Error>
where
    D: serde::Deserializer<'de>,
    T: serde::Deserialize<'de>,
{
    Ok(Option::<Vec<T>>::deserialize(deserializer)?.unwrap_or_default())
}

/// Go: `retention.Proposal`.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct Proposal {
    pub run_id: String,
    pub rule_name: String,
    #[serde(with = "crate::history::rfc3339_nano")]
    pub created: OffsetDateTime,
    pub items: Vec<ProposalItem>,
    pub status: String,
}

/// Go: `retention.ProposalItem`.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct ProposalItem {
    pub path: String,
    pub title: String,
    pub reference_date: String,
    pub expires_at: String,
    pub action: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub rule_name: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub fingerprint: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub status: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub failure: String,
}

/// Go: `retention.HistoryEntry`.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct HistoryEntry {
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub action_id: String,
    #[serde(with = "crate::history::rfc3339_nano")]
    pub timestamp: OffsetDateTime,
    pub rule_name: String,
    pub action: String,
    pub path: String,
    pub title: String,
}

/// Everything that can go wrong. The messages are Go's, verbatim, because the
/// vectors compare them.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum RetentionError {
    /// `Validate` or `ValidateRunID` rejected the input.
    Validation(String),
    /// A state file could not be read.
    ReadFailed,
    /// A state file did not decode.
    DecodeFailed,
    /// A wrapped filesystem failure, already prefixed like Go.
    Message(String),
}

impl RetentionError {
    /// The class the differential vectors compare for failures whose wording is
    /// language-specific.
    pub fn class(&self) -> &'static str {
        match self {
            Self::Validation(_) => "validation",
            Self::ReadFailed => "read_failed",
            Self::DecodeFailed => "decode_failed",
            Self::Message(_) => "other",
        }
    }
}

impl std::fmt::Display for RetentionError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            Self::Validation(message) | Self::Message(message) => f.write_str(message),
            Self::ReadFailed => f.write_str("read failed"),
            Self::DecodeFailed => f.write_str("decode failed"),
        }
    }
}

impl std::error::Error for RetentionError {}

/// Go: `retention.Validate`.
pub fn validate(rule: &Rule) -> Result<(), RetentionError> {
    if rule.name.trim().is_empty() {
        return Err(RetentionError::Validation(
            "rule name is required".to_owned(),
        ));
    }
    if rule.period_days <= 0 {
        return Err(RetentionError::Validation(
            "period_days must be positive".to_owned(),
        ));
    }
    let field = if rule.reference_field.is_empty() {
        "document_date"
    } else {
        rule.reference_field.as_str()
    };
    if !valid_reference_field(field) {
        return Err(RetentionError::Validation(format!(
            "unsupported reference field {}",
            go_quote(field)
        )));
    }
    if !valid_action(&rule.action) {
        return Err(RetentionError::Validation(format!(
            "unsupported action {} (valid: trash, flag_review)",
            go_quote(&rule.action)
        )));
    }
    Ok(())
}

/// Go: `retention.LoadRules`. Reads a multi-document YAML rules file: the file
/// is split on a `---` line, empty chunks and chunks starting with `#` are
/// skipped, every remaining chunk is parsed as one rule, validated, and its
/// `period` is derived from `period_days` in nanoseconds. The chunk handling
/// mirrors Go instead of a YAML multi-document reader, because Go splits the
/// bytes before parsing.
pub fn load_rules(path: &Path) -> Result<Vec<Rule>, RetentionError> {
    let data = std::fs::read(path).map_err(|_| RetentionError::ReadFailed)?;
    let content = String::from_utf8_lossy(&data);
    let mut rules = Vec::new();
    for chunk in content.split("\n---\n") {
        let document = chunk.trim();
        if document.is_empty() || document.starts_with('#') {
            continue;
        }
        let mut rule: Rule = noyalib::from_slice(document.as_bytes())
            .map_err(|err| RetentionError::Message(format!("parse retention rule: {err}")))?;
        if let Err(err) = validate(&rule) {
            // Go wraps a rule failure with the rule's name: `invalid rule "x": …`.
            return Err(RetentionError::Validation(format!(
                "invalid rule {}: {err}",
                go_quote(&rule.name)
            )));
        }
        rule.period = rule.period_days * 24 * 60 * 60 * 1_000_000_000;
        rules.push(rule);
    }
    Ok(rules)
}

/// Go: `retention.DocMetaFromDocument`. Extracts a DocMeta from a
/// vault Document, pulling `correspondent` and `document_type` from
/// frontmatter as strings (non-string values yield "").
pub fn doc_meta_from_document(doc: &crate::Document) -> DocMeta {
    DocMeta {
        path: doc.path.clone(),
        title: doc.title.clone(),
        document_date: doc.document_date.clone(),
        created: doc.created.clone(),
        due_date: doc.due_date.clone(),
        status: doc.status.clone(),
        correspondent: frontmatter_string(&doc.frontmatter, "correspondent"),
        document_type: frontmatter_string(&doc.frontmatter, "document_type"),
        person: doc.person.clone(),
        tags: doc.tags.clone(),
    }
}

/// Extract a frontmatter value as a string, returning "" if the key is
/// absent or the value is not a string — matching Go's
/// `extractFrontmatterString`.
fn frontmatter_string(
    frontmatter: &std::collections::BTreeMap<String, noyalib::Value>,
    key: &str,
) -> String {
    match frontmatter.get(key) {
        Some(noyalib::Value::String(s)) => s.clone(),
        _ => String::new(),
    }
}

/// Go: `retention.Rule.Period` derived from `period_days`.
pub fn period_from_days(period_days: i64) -> Duration {
    Duration::days(period_days)
}

fn valid_action(action: &str) -> bool {
    action == ACTION_TRASH || action == ACTION_FLAG_REVIEW
}

fn valid_reference_field(field: &str) -> bool {
    matches!(field, "document_date" | "created" | "due_date")
}

/// Go: `retention.ValidateRunID`, including the platform-safety rules.
pub fn validate_run_id(run_id: &str) -> Result<(), RetentionError> {
    if run_id.is_empty() || run_id.trim().is_empty() {
        return Err(RetentionError::Validation(
            "retention proposal run ID must not be empty".to_owned(),
        ));
    }
    if run_id == "." || run_id == ".." || run_id.contains('/') || run_id.contains('\\') {
        return Err(RetentionError::Validation(format!(
            "retention proposal run ID {} is not a single safe filename component",
            go_quote(run_id)
        )));
    }
    if run_id.chars().any(char::is_control) {
        return Err(RetentionError::Validation(format!(
            "retention proposal run ID {} contains a control character",
            go_quote(run_id)
        )));
    }
    if run_id.contains(['<', '>', ':', '"', '|', '?', '*'])
        || run_id.ends_with('.')
        || run_id.ends_with(' ')
    {
        return Err(RetentionError::Validation(format!(
            "retention proposal run ID {} is not platform-safe",
            go_quote(run_id)
        )));
    }
    if is_windows_reserved_name(&run_id.to_uppercase()) {
        return Err(RetentionError::Validation(format!(
            "retention proposal run ID {} is a reserved platform name",
            go_quote(run_id)
        )));
    }
    Ok(())
}

fn is_windows_reserved_name(name: &str) -> bool {
    let name = match name.find('.') {
        Some(dot) => &name[..dot],
        None => name,
    };
    if matches!(name, "CON" | "PRN" | "AUX" | "NUL") {
        return true;
    }
    if name.len() == 4 && (name.starts_with("COM") || name.starts_with("LPT")) {
        return name.as_bytes()[3].is_ascii_digit() && name.as_bytes()[3] != b'0';
    }
    false
}

/// Go: `DocMeta.ReferenceDate`. A date-only value parses as midnight UTC, a full
/// RFC 3339 value keeps its instant; anything else is not a reference date.
pub fn reference_date(doc: &DocMeta, field: &str) -> Option<OffsetDateTime> {
    let raw = match field {
        "document_date" => &doc.document_date,
        "created" => &doc.created,
        "due_date" => &doc.due_date,
        _ => return None,
    };
    if raw.is_empty() {
        return None;
    }
    // Go's `time.Parse("2006-01-02", raw)` insists on the exact date layout, so
    // the port checks the shape before parsing; `Iso8601::DATE` alone would also
    // accept the leading date of a full timestamp.
    if raw.len() == 10
        && raw.as_bytes()[4] == b'-'
        && raw.as_bytes()[7] == b'-'
        && let Ok(date) =
            time::Date::parse(raw, &time::format_description::well_known::Iso8601::DATE)
    {
        return Some(date.midnight().assume_utc());
    }
    OffsetDateTime::parse(raw, &Rfc3339).ok()
}

/// Go: `Selector.Matches`.
///
/// ponytail: the comparisons fold ASCII case exactly like Go's
/// `strings.EqualFold` does for ASCII, and like Go they are Unicode-folded for
/// the rest — `to_lowercase` is the closest std equivalent; the vectors cover
/// ASCII only.
pub fn matches(selector: &Selector, doc: &DocMeta) -> bool {
    if !selector.document_type.is_empty() && !eq_fold(&selector.document_type, &doc.document_type) {
        return false;
    }
    if !selector.status.is_empty() && !eq_fold(&selector.status, &doc.status) {
        return false;
    }
    if !selector.person.is_empty() && !eq_fold(&selector.person, &doc.person) {
        return false;
    }
    if !selector.correspondent.is_empty() && !eq_fold(&selector.correspondent, &doc.correspondent) {
        return false;
    }
    // Category has no counterpart in DocMeta in Go either: the check is skipped.
    if !selector.tags.is_empty() {
        let mut tag_set: Vec<String> = doc.tags.iter().map(|tag| tag.to_lowercase()).collect();
        tag_set.sort();
        for tag in &selector.tags {
            if tag_set.binary_search(&tag.to_lowercase()).is_err() {
                return false;
            }
        }
    }
    true
}

fn eq_fold(left: &str, right: &str) -> bool {
    left.to_lowercase() == right.to_lowercase()
}

/// Go: `retention.Evaluate`. An item expires when `now` is not before the
/// reference date plus the period, so an expiry equal to `now` counts as
/// expired; documents without a parsable reference date are skipped.
pub fn evaluate(rule: &Rule, docs: &[DocMeta], now: OffsetDateTime) -> Vec<ProposalItem> {
    let field = if rule.reference_field.is_empty() {
        "document_date"
    } else {
        rule.reference_field.as_str()
    };
    let period = if rule.period != 0 {
        Duration::nanoseconds(rule.period)
    } else {
        period_from_days(rule.period_days)
    };

    let mut items = Vec::new();
    for doc in docs {
        if !matches(&rule.selector, doc) {
            continue;
        }
        let Some(reference) = reference_date(doc, field) else {
            continue;
        };
        let expires_at = reference + period;
        if now < expires_at {
            continue;
        }
        items.push(ProposalItem {
            path: doc.path.clone(),
            title: doc.title.clone(),
            reference_date: format_date(reference),
            expires_at: format_date(expires_at),
            action: rule.action.clone(),
            rule_name: String::new(),
            fingerprint: String::new(),
            status: String::new(),
            failure: String::new(),
        });
    }
    items
}

/// Go: `time.Format("2006-01-02")`.
pub fn format_date(value: OffsetDateTime) -> String {
    let utc = value.to_offset(time::UtcOffset::UTC);
    format!(
        "{:04}-{:02}-{:02}",
        utc.year(),
        u8::from(utc.month()),
        utc.day()
    )
}

/// Go: `retention.ProposalDir`.
pub fn proposal_dir(vault_root: &Path) -> PathBuf {
    vault_root.join(".symdesk").join("retention")
}

/// Go: `retention.HistoryPath`.
pub fn history_path(vault_root: &Path) -> PathBuf {
    proposal_dir(vault_root).join("history.json")
}

/// Go: `retention.StableActionID`.
pub fn stable_action_id(run_id: &str, item_index: usize) -> String {
    format!("{run_id}:{item_index}")
}

/// Go: `retention.WriteProposal` — validated run id, 0755 state directory,
/// two-space indented JSON written atomically with mode 0644.
pub fn write_proposal(vault_root: &Path, proposal: &Proposal) -> Result<(), RetentionError> {
    validate_run_id(&proposal.run_id)?;
    let dir = proposal_dir(vault_root);
    std::fs::create_dir_all(&dir).map_err(|err| RetentionError::Message(err.to_string()))?;
    restrict_mode(&dir, 0o755);
    let data = serde_json::to_vec_pretty(proposal)
        .map_err(|err| RetentionError::Message(err.to_string()))?;
    write_file_atomic(&dir.join(format!("{}.json", proposal.run_id)), &data, 0o644)
}

/// Go: `retention.LoadProposal`.
pub fn load_proposal(vault_root: &Path, run_id: &str) -> Result<Proposal, RetentionError> {
    validate_run_id(run_id)?;
    let path = proposal_dir(vault_root).join(format!("{run_id}.json"));
    let data = std::fs::read(&path).map_err(|_| RetentionError::ReadFailed)?;
    serde_json::from_slice(&data).map_err(|_| RetentionError::DecodeFailed)
}

/// Go: `retention.AppendHistory` — idempotent for entries carrying an action id,
/// the older format still appends.
pub fn append_history(vault_root: &Path, entry: &HistoryEntry) -> Result<(), RetentionError> {
    let path = history_path(vault_root);
    std::fs::create_dir_all(proposal_dir(vault_root))
        .map_err(|err| RetentionError::Message(err.to_string()))?;
    let mut entries = load_history(vault_root)?;
    if !entry.action_id.is_empty()
        && entries
            .iter()
            .any(|existing| existing.action_id == entry.action_id)
    {
        return Ok(());
    }
    entries.push(entry.clone());
    let data = serde_json::to_vec_pretty(&entries)
        .map_err(|err| RetentionError::Message(err.to_string()))?;
    write_file_atomic(&path, &data, 0o644)
}

/// Go: `retention.LoadHistory` — a missing file is an empty log, a `null` or
/// non-array document is rejected rather than silently treated as empty.
pub fn load_history(vault_root: &Path) -> Result<Vec<HistoryEntry>, RetentionError> {
    let path = history_path(vault_root);
    let data = match std::fs::read(&path) {
        Ok(data) => data,
        Err(err) if err.kind() == std::io::ErrorKind::NotFound => return Ok(Vec::new()),
        Err(err) => return Err(RetentionError::Message(err.to_string())),
    };
    if serde_json::from_slice::<serde_json::Value>(&data)
        .map(|value| value.is_null())
        .unwrap_or(false)
    {
        return Err(RetentionError::Validation(
            "retention history must be a non-null array".to_owned(),
        ));
    }
    serde_json::from_slice(&data).map_err(|_| RetentionError::DecodeFailed)
}

/// Go: `retention.writeFileAtomic` — a complete file beside its target, synced
/// and renamed into place so a reader never sees a partial document.
fn write_file_atomic(path: &Path, data: &[u8], perm: u32) -> Result<(), RetentionError> {
    use std::io::Write;

    let dir = path.parent().unwrap_or_else(|| Path::new("."));
    let (mut file, tmp_path) = temp_file_in(dir)?;
    if let Err(err) = file.write_all(data).and_then(|()| file.sync_all()) {
        drop(file);
        let _ = std::fs::remove_file(&tmp_path);
        return Err(RetentionError::Message(err.to_string()));
    }
    drop(file);
    restrict_mode(&tmp_path, perm);
    if let Err(err) = std::fs::rename(&tmp_path, path) {
        let _ = std::fs::remove_file(&tmp_path);
        return Err(RetentionError::Message(err.to_string()));
    }
    Ok(())
}

/// A uniquely named scratch file in `dir`, mirroring Go's `os.CreateTemp` with
/// the `.symdesk-retention-*.tmp` pattern.
fn temp_file_in(dir: &Path) -> Result<(std::fs::File, PathBuf), RetentionError> {
    use std::time::{SystemTime, UNIX_EPOCH};

    let nanos = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|value| value.as_nanos())
        .unwrap_or_default();
    for attempt in 0..64u32 {
        let candidate = dir.join(format!(".symdesk-retention-{nanos:x}-{attempt}.tmp"));
        match std::fs::OpenOptions::new()
            .write(true)
            .create_new(true)
            .open(&candidate)
        {
            Ok(file) => return Ok((file, candidate)),
            Err(err) if err.kind() == std::io::ErrorKind::AlreadyExists => continue,
            Err(err) => return Err(RetentionError::Message(err.to_string())),
        }
    }
    Err(RetentionError::Message(
        "could not create a temporary retention file".to_owned(),
    ))
}

#[cfg(unix)]
fn restrict_mode(path: &Path, mode: u32) {
    use std::os::unix::fs::PermissionsExt;
    let _ = std::fs::set_permissions(path, std::fs::Permissions::from_mode(mode));
}

#[cfg(not(unix))]
fn restrict_mode(_path: &Path, _mode: u32) {
    // Windows carries no Unix permission bits.
}

/// Go's `%q` for the strings the retention messages quote: double quotes,
/// backslashes, the common escapes and `\xNN` for other control characters.
///
/// ponytail: Go also escapes non-printable Unicode as `\u…`, which the vectors
/// do not exercise.
pub fn go_quote(value: &str) -> String {
    let mut out = String::with_capacity(value.len() + 2);
    out.push('"');
    for character in value.chars() {
        match character {
            '"' => out.push_str("\\\""),
            '\\' => out.push_str("\\\\"),
            '\n' => out.push_str("\\n"),
            '\r' => out.push_str("\\r"),
            '\t' => out.push_str("\\t"),
            other if (other as u32) < 0x20 || other as u32 == 0x7f => {
                out.push_str(&format!("\\x{:02x}", other as u32));
            }
            other => out.push(other),
        }
    }
    out.push('"');
    out
}
