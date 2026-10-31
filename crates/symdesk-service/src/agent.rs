//! Go `internal/ai` agentic loop: the bounded read-only tool loop, tool-result
//! externalization (issue #406), advisory citation validation (issue #408) and
//! the `AIEvent` stream that `symdesk ask` prints.
//!
//! Tool execution and event delivery are injected, so the CLI, MCP and HTTP
//! facades share one loop and tests never touch a real provider.

use std::{
    collections::BTreeMap,
    fs,
    io::Write,
    path::{Path, PathBuf},
    time::{Duration, SystemTime, UNIX_EPOCH},
};

use serde::Serialize;
use serde_json::{Value, value::RawValue};
use symdesk_core::{
    agent_turn::{self, Kind, Message, ToolCall, ToolDef, TurnRequest},
    ai::NOT_CONFIGURED,
    cancel::CancelToken,
    config::Config,
    gofmt::{compact_json, float_v, json_syntax_error, lossy},
};

use crate::ai::{Context, Doc};

/// Output cap echoed back into the conversation (Go `maxToolOutputChars`).
const MAX_TOOL_OUTPUT: usize = 8000;
/// Inline preview of an externalized result (Go `externalizePreviewChars`).
const EXTERNALIZE_PREVIEW: usize = 4000;
const DEFAULT_MAX_ITERATIONS: i64 = 5;
const DEFAULT_OLLAMA_MODEL: &str = "llama3.2";
const DEFAULT_ANTHROPIC_MODEL: &str = "claude-sonnet-5";

// ---------------------------------------------------------------------------
// Events
// ---------------------------------------------------------------------------

/// A citation-shaped link in generated content that was not read in this run.
#[derive(Clone, Debug, Eq, PartialEq, Serialize)]
pub struct CitationWarning {
    pub path: String,
    #[serde(skip_serializing_if = "is_zero_i64")]
    pub line: i64,
}

#[allow(clippy::trivially_copy_pass_by_ref)]
fn is_zero_i64(value: &i64) -> bool {
    *value == 0
}

#[allow(clippy::trivially_copy_pass_by_ref)]
fn is_zero_f64(value: &f64) -> bool {
    *value == 0.0
}

/// Go `ai.AIEvent`, field for field.
#[derive(Clone, Debug, Default)]
pub struct AiEvent {
    pub kind: String,
    pub text: String,
    pub path: String,
    pub title: String,
    pub snippet: String,
    pub score: f64,
    pub tool_name: String,
    pub status: String,
    pub iteration: i64,
    /// Raw JSON tool arguments (`json.RawMessage`); empty when absent.
    pub tool_inputs: String,
    /// May carry invalid UTF-8 (Go cuts tool output at a byte offset).
    pub tool_output: Vec<u8>,
    pub tool_output_truncated: bool,
    pub citation_warnings: Vec<CitationWarning>,
    pub read_paths: Vec<String>,
    pub token_usage: i64,
    pub context_window: i64,
}

#[derive(Serialize)]
struct Wire<'a> {
    #[serde(rename = "type")]
    kind: &'a str,
    #[serde(skip_serializing_if = "str::is_empty")]
    text: &'a str,
    #[serde(skip_serializing_if = "str::is_empty")]
    path: &'a str,
    #[serde(skip_serializing_if = "str::is_empty")]
    title: &'a str,
    #[serde(skip_serializing_if = "str::is_empty")]
    snippet: &'a str,
    #[serde(skip_serializing_if = "is_zero_f64")]
    score: f64,
    #[serde(skip_serializing_if = "str::is_empty")]
    tool_name: &'a str,
    #[serde(skip_serializing_if = "str::is_empty")]
    status: &'a str,
    #[serde(skip_serializing_if = "is_zero_i64")]
    iteration: i64,
    #[serde(skip_serializing_if = "Option::is_none")]
    tool_inputs: Option<Box<RawValue>>,
    #[serde(skip_serializing_if = "str::is_empty")]
    tool_output: &'a str,
    #[serde(skip_serializing_if = "std::ops::Not::not")]
    tool_output_truncated: bool,
    #[serde(skip_serializing_if = "<[CitationWarning]>::is_empty")]
    citation_warnings: &'a [CitationWarning],
    #[serde(skip_serializing_if = "<[String]>::is_empty")]
    read_paths: &'a [String],
    #[serde(skip_serializing_if = "is_zero_i64")]
    token_usage: i64,
    #[serde(skip_serializing_if = "is_zero_i64")]
    context_window: i64,
}

fn go_escape(rendered: String) -> String {
    rendered
        .replace('&', "\\u0026")
        .replace('<', "\\u003c")
        .replace('>', "\\u003e")
        .replace('\u{2028}', "\\u2028")
        .replace('\u{2029}', "\\u2029")
}

impl AiEvent {
    fn of(kind: &str) -> Self {
        Self {
            kind: kind.to_owned(),
            ..Self::default()
        }
    }

    #[must_use]
    pub fn answer(text: &str) -> Self {
        Self {
            text: text.to_owned(),
            ..Self::of("answer")
        }
    }

    #[must_use]
    pub fn citation(path: &str, title: &str, snippet: &str, score: f64) -> Self {
        Self {
            path: path.to_owned(),
            title: title.to_owned(),
            snippet: snippet.to_owned(),
            score,
            ..Self::of("citation")
        }
    }

    #[must_use]
    pub fn tool(name: &str, status: &str) -> Self {
        Self {
            tool_name: name.to_owned(),
            status: status.to_owned(),
            ..Self::of("tool")
        }
    }

    #[must_use]
    pub fn done() -> Self {
        Self::of("done")
    }

    fn tool_call(iteration: i64, name: &str, inputs: &str) -> Self {
        Self {
            tool_name: name.to_owned(),
            iteration,
            tool_inputs: inputs.to_owned(),
            ..Self::of("tool")
        }
    }

    fn tool_result(iteration: i64, name: &str, output: Vec<u8>, truncated: bool) -> Self {
        Self {
            tool_name: name.to_owned(),
            iteration,
            tool_output: output,
            tool_output_truncated: truncated,
            ..Self::of("tool")
        }
    }

    fn terminal(token_usage: i64, context_window: i64) -> Self {
        Self {
            token_usage,
            context_window,
            ..Self::of("done")
        }
    }

    /// One NDJSON line as `json.NewEncoder(os.Stdout).Encode` writes it, minus
    /// the newline.
    ///
    /// # Errors
    ///
    /// Go's `MarshalJSON` failure for an invalid `tool_inputs` raw message.
    pub fn json_line(&self) -> Result<String, String> {
        let tool_inputs = if self.tool_inputs.is_empty() {
            None
        } else {
            Some(
                RawValue::from_string(compact_json(&self.tool_inputs)).map_err(|_| {
                    format!(
                        "json: error calling MarshalJSON for type json.RawMessage: {}",
                        json_syntax_error(&self.tool_inputs)
                    )
                })?,
            )
        };
        let output = lossy(&self.tool_output);
        let wire = Wire {
            kind: &self.kind,
            text: &self.text,
            path: &self.path,
            title: &self.title,
            snippet: &self.snippet,
            score: self.score,
            tool_name: &self.tool_name,
            status: &self.status,
            iteration: self.iteration,
            tool_inputs,
            tool_output: &output,
            tool_output_truncated: self.tool_output_truncated,
            citation_warnings: &self.citation_warnings,
            read_paths: &self.read_paths,
            token_usage: self.token_usage,
            context_window: self.context_window,
        };
        serde_json::to_string(&wire)
            .map(go_escape)
            .map_err(|error| error.to_string())
    }

    /// `fmt.Printf("%v", event)` of the whole struct, as bytes (the tool output
    /// is printed raw).
    #[must_use]
    pub fn go_value_format(&self) -> Vec<u8> {
        let inputs = self
            .tool_inputs
            .bytes()
            .map(|byte| byte.to_string())
            .collect::<Vec<_>>()
            .join(" ");
        let warnings = self
            .citation_warnings
            .iter()
            .map(|warning| format!("{{{} {}}}", warning.path, warning.line))
            .collect::<Vec<_>>()
            .join(" ");
        let mut out = format!(
            "{{{} {} {} {} {} {} {} {} {} [{inputs}] ",
            self.kind,
            self.text,
            self.path,
            self.title,
            self.snippet,
            float_v(self.score),
            self.tool_name,
            self.status,
            self.iteration,
        )
        .into_bytes();
        out.extend_from_slice(&self.tool_output);
        out.extend_from_slice(
            format!(
                " {} [{warnings}] [{}] {} {}}}",
                self.tool_output_truncated,
                self.read_paths.join(" "),
                self.token_usage,
                self.context_window
            )
            .as_bytes(),
        );
        out
    }
}

// ---------------------------------------------------------------------------
// Citation validation (Go internal/ai/citations.go)
// ---------------------------------------------------------------------------

const BODY_SCAN_LINE_LIMIT: usize = 4000;
const FRONTMATTER_LINE_LIMIT: usize = 200;
const SOURCE_KEYS: [&str; 8] = [
    "quelle",
    "quellen",
    "source",
    "sources",
    "referenz",
    "referenzen",
    "reference",
    "references",
];
const DOCUMENT_EXTENSIONS: [&str; 14] = [
    ".csv",
    ".doc",
    ".docx",
    ".eml",
    ".md",
    ".markdown",
    ".odt",
    ".pdf",
    ".ppt",
    ".pptx",
    ".rtf",
    ".txt",
    ".xls",
    ".xlsx",
];
const DOCUMENT_WORDS: [&str; 14] = [
    "interview",
    "notiz",
    "note",
    "meeting",
    "bericht",
    "report",
    "use case",
    "use-case",
    "protokoll",
    "document",
    "dokument",
    "briefing",
    "memo",
    "synthese",
];
const CITATION_COLUMN_WORDS: [&str; 12] = [
    "gesprächspartner",
    "gespraechspartner",
    "interview",
    "quelle",
    "source",
    "verfasser",
    "author",
    "speaker",
    "sprecher",
    "befragter",
    "zitat",
    "citation",
];

struct Candidate {
    path: String,
    line: i64,
}

/// Citation-shaped wikilinks to unread files in generated Markdown. Warnings
/// are advisory and never block a write.
#[must_use]
pub fn check_citation_warnings(content: &str, read_paths: &[String]) -> Vec<CitationWarning> {
    let read: std::collections::BTreeSet<String> = read_paths
        .iter()
        .map(|path| citation_path_key(path))
        .filter(|key| !key.is_empty())
        .collect();
    let mut candidates = Vec::new();
    let body_start = scan_frontmatter(content, &mut candidates);
    scan_body(content, body_start, &mut candidates);
    let mut seen = std::collections::BTreeSet::new();
    let mut warnings = Vec::new();
    for candidate in candidates {
        let key = citation_path_key(&candidate.path);
        if key.is_empty() || read.contains(&key) || !seen.insert(key) {
            continue;
        }
        warnings.push(CitationWarning {
            path: candidate.path,
            line: candidate.line,
        });
    }
    warnings
}

fn scan_frontmatter(content: &str, candidates: &mut Vec<Candidate>) -> usize {
    let lines: Vec<&str> = content.splitn(FRONTMATTER_LINE_LIMIT + 2, '\n').collect();
    if lines.first().is_none_or(|first| first.trim() != "---") {
        return 0;
    }
    let mut in_source_block = false;
    for (index, line) in lines.iter().enumerate().skip(1) {
        if index > FRONTMATTER_LINE_LIMIT {
            break;
        }
        if line.trim() == "---" {
            return index + 1;
        }
        if let Some((key, value)) = frontmatter_key(line) {
            in_source_block = SOURCE_KEYS.contains(&key.trim().to_lowercase().as_str());
            if in_source_block {
                append_candidates(
                    candidates,
                    value,
                    i64::try_from(index + 1).unwrap_or(0),
                    false,
                );
            }
            continue;
        }
        if in_source_block && line.trim().contains("[[") {
            append_candidates(
                candidates,
                line,
                i64::try_from(index + 1).unwrap_or(0),
                false,
            );
        }
    }
    0
}

fn frontmatter_key(line: &str) -> Option<(&str, &str)> {
    let trimmed = line.trim();
    let colon = trimmed.find(':')?;
    if colon == 0 {
        return None;
    }
    Some((trimmed[..colon].trim(), &trimmed[colon + 1..]))
}

/// `bufio.ScanLines`: split on `\n`, drop one trailing `\r`, no final empty
/// element.
fn scan_lines(content: &str) -> impl Iterator<Item = &str> {
    let mut parts: Vec<&str> = content.split('\n').collect();
    if parts.last().is_some_and(|last| last.is_empty()) {
        parts.pop();
    }
    parts
        .into_iter()
        .map(|line| line.strip_suffix('\r').unwrap_or(line))
}

fn scan_body(content: &str, body_start: usize, candidates: &mut Vec<Candidate>) {
    let mut in_source_heading = false;
    let mut in_table = false;
    let mut citation_column: Option<usize> = None;
    for (absolute, line) in scan_lines(content).enumerate() {
        if absolute < body_start {
            continue;
        }
        if absolute - body_start >= BODY_SCAN_LINE_LIMIT {
            break;
        }
        let number = i64::try_from(absolute + 1).unwrap_or(0);
        if line.trim().starts_with('#') && is_markdown_heading(line) {
            in_source_heading = is_citation_heading(line);
            in_table = false;
            citation_column = None;
            continue;
        }
        if in_source_heading {
            let trimmed = line.trim();
            if trimmed.is_empty() {
                continue;
            } else if trimmed.starts_with('-') || trimmed.starts_with('*') {
                append_candidates(candidates, line, number, true);
                continue;
            }
            in_source_heading = false;
        }
        if line.contains('|') {
            let cells = table_cells(line);
            if !in_table {
                for (column, cell) in cells.iter().enumerate() {
                    if is_citation_column(cell) {
                        citation_column = Some(column);
                        in_table = true;
                        break;
                    }
                }
                continue;
            }
            if is_table_separator(line) {
                continue;
            }
            if let Some(cell) = citation_column.and_then(|column| cells.get(column)) {
                append_candidates(candidates, cell, number, true);
            }
        } else {
            in_table = false;
            citation_column = None;
        }
    }
}

fn append_candidates(candidates: &mut Vec<Candidate>, text: &str, line: i64, documents_only: bool) {
    let mut start = 0;
    while start < text.len() {
        let Some(open) = text[start..].find("[[") else {
            return;
        };
        let open = open + start;
        let Some(close) = text[open + 2..].find("]]") else {
            return;
        };
        let close = close + open + 2;
        let mut target = text[open + 2..close].trim();
        if let Some(pipe) = target.find('|') {
            target = &target[..pipe];
        }
        if let Some(anchor) = target.find(['#', '^']) {
            target = &target[..anchor];
        }
        let target = target.trim();
        if !target.is_empty() && (!documents_only || looks_like_document_reference(target)) {
            candidates.push(Candidate {
                path: target.to_owned(),
                line,
            });
        }
        start = close + 2;
    }
}

fn is_markdown_heading(line: &str) -> bool {
    let trimmed = line.trim_start_matches([' ', '\t']);
    let level = trimmed.bytes().take_while(|byte| *byte == b'#').count();
    level > 0 && level < trimmed.len() && matches!(trimmed.as_bytes()[level], b' ' | b'\t')
}

fn is_citation_heading(line: &str) -> bool {
    let lower = line.trim().trim_start_matches('#').trim().to_lowercase();
    SOURCE_KEYS.contains(&lower.as_str())
}

fn is_citation_column(cell: &str) -> bool {
    let lower = cell.to_lowercase();
    CITATION_COLUMN_WORDS
        .iter()
        .any(|word| lower.contains(word))
}

fn table_cells(line: &str) -> Vec<String> {
    let mut parts: Vec<&str> = line.split('|').collect();
    if parts.len() > 1 && parts[0].trim().is_empty() {
        parts.remove(0);
    }
    if parts.len() > 1 && parts[parts.len() - 1].trim().is_empty() {
        parts.pop();
    }
    parts.iter().map(|part| part.trim().to_owned()).collect()
}

fn is_table_separator(line: &str) -> bool {
    let cells = table_cells(line);
    !cells.is_empty()
        && cells
            .iter()
            .all(|cell| cell.trim_matches(['-', ':']).trim().is_empty())
}

fn go_ext(path: &str) -> &str {
    let base = path.rsplit('/').next().unwrap_or(path);
    base.rfind('.').map_or("", |dot| &base[dot..])
}

fn looks_like_document_reference(target: &str) -> bool {
    let ext = go_ext(target).to_lowercase();
    if !ext.is_empty() && DOCUMENT_EXTENSIONS.contains(&ext.as_str()) {
        return true;
    }
    let lower = target.to_lowercase();
    DOCUMENT_WORDS.iter().any(|word| lower.contains(word))
}

/// Lexical `filepath.Clean` for slash-separated paths.
fn go_clean(path: &str) -> String {
    if path.is_empty() {
        return ".".to_owned();
    }
    let rooted = path.starts_with('/');
    let mut out: Vec<&str> = Vec::new();
    for part in path.split('/') {
        match part {
            "" | "." => {}
            ".." => {
                if out.last().is_some_and(|last| *last != "..") {
                    out.pop();
                } else if !rooted {
                    out.push("..");
                }
            }
            part => out.push(part),
        }
    }
    let joined = out.join("/");
    if rooted {
        format!("/{joined}")
    } else if joined.is_empty() {
        ".".to_owned()
    } else {
        joined
    }
}

fn clean_read_path(path: &str) -> String {
    let path = path.trim();
    let path = path.strip_prefix("./").unwrap_or(path).trim();
    if path.is_empty() || path.contains(['\r', '\n']) || path.contains("://") {
        return String::new();
    }
    let cleaned = go_clean(path);
    if cleaned == "." || cleaned == ".." || cleaned.starts_with("../") {
        return String::new();
    }
    cleaned
}

fn citation_path_key(path: &str) -> String {
    let path = clean_read_path(path);
    if path.is_empty() {
        return String::new();
    }
    let base = path.rsplit('/').next().unwrap_or(&path);
    let stripped = match base.rfind('.') {
        Some(dot) if dot > 0 => &path[..path.len() - base.len() + dot],
        _ => &path,
    };
    stripped.to_lowercase()
}

/// Document paths a tool call touched: `path`/`file`/`note` strings in its
/// input and in its (JSON) output.
#[must_use]
pub fn read_paths_for_tool(input: &str, output: &str) -> Vec<String> {
    let mut paths = std::collections::BTreeSet::new();
    for text in [input, output] {
        if let Ok(value) = serde_json::from_str::<Value>(text) {
            collect_document_paths(&value, &mut paths);
        }
    }
    paths.into_iter().collect()
}

fn collect_document_paths(value: &Value, paths: &mut std::collections::BTreeSet<String>) {
    match value {
        Value::Object(map) => {
            for (key, child) in map {
                if matches!(key.as_str(), "path" | "file" | "note") {
                    if let Some(path) = child.as_str() {
                        let cleaned = clean_read_path(path);
                        if !cleaned.is_empty() {
                            paths.insert(cleaned);
                        }
                    }
                }
                collect_document_paths(child, paths);
            }
        }
        Value::Array(items) => {
            for child in items {
                collect_document_paths(child, paths);
            }
        }
        _ => {}
    }
}

// ---------------------------------------------------------------------------
// Externalizer (Go internal/ai/externalize.go)
// ---------------------------------------------------------------------------

const DEFAULT_EXTERNALIZE_THRESHOLD: usize = 8000;
const SEARCH_EXTERNALIZE_THRESHOLD: usize = 10_000;
const EXTERNALIZE_SKIP: [&str; 13] = [
    "desk_status",
    "desk_ask",
    "desk_transform",
    "desk_read_result",
    "desk_note_new",
    "desk_export",
    "desk_clip",
    "desk_autofill",
    "desk_ingest",
    "desk_ingest_retry",
    "desk_doc_set_status",
    "meeting_import",
    "desk_meeting_import",
];

/// Writes over-threshold tool results below `<root>/<task>/<tool>-<nnn>.txt`.
pub struct Externalizer {
    root: PathBuf,
    task_id: String,
}

impl Externalizer {
    #[must_use]
    pub fn new(root: PathBuf, task_id: String) -> Self {
        Self { root, task_id }
    }

    #[must_use]
    pub fn should_externalize(&self, tool: &str, size: usize) -> bool {
        if EXTERNALIZE_SKIP.contains(&tool) {
            return false;
        }
        let threshold = if tool == "desk_search" {
            SEARCH_EXTERNALIZE_THRESHOLD
        } else {
            DEFAULT_EXTERNALIZE_THRESHOLD
        };
        size > threshold
    }

    #[must_use]
    pub fn handle(&self, tool: &str, iteration: i64) -> String {
        format!("{}/{tool}-{iteration:03}.txt", self.task_id)
    }

    /// # Errors
    ///
    /// Directory or file creation failures; the caller keeps the output inline.
    pub fn externalize(
        &self,
        tool: &str,
        iteration: i64,
        output: &[u8],
    ) -> std::io::Result<String> {
        let handle = self.handle(tool, iteration);
        let path = self.root.join(&handle);
        if let Some(parent) = path.parent() {
            create_private_dir(parent)?;
        }
        write_private_file(&path, output)?;
        Ok(handle)
    }
}

fn create_private_dir(path: &Path) -> std::io::Result<()> {
    let mut builder = fs::DirBuilder::new();
    builder.recursive(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::DirBuilderExt;
        builder.mode(0o700);
    }
    builder.create(path)
}

fn write_private_file(path: &Path, data: &[u8]) -> std::io::Result<()> {
    let mut options = fs::OpenOptions::new();
    options.write(true).create(true).truncate(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        options.mode(0o600);
    }
    options.open(path)?.write_all(data)
}

/// Go `newTaskID`: unique and time-ordered per run.
#[must_use]
pub fn new_task_id() -> String {
    let nanos = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map_or(0, |elapsed| elapsed.as_nanos());
    format!("task-{nanos}")
}

/// Go `PruneResults`: keep the newest `max_per_task` results per task and drop
/// anything older than `max_age`; emptied task directories are removed.
///
/// # Errors
///
/// The first filesystem failure; a missing root is a no-op.
pub fn prune_results(
    root: &Path,
    max_age: Option<Duration>,
    max_per_task: usize,
) -> std::io::Result<usize> {
    let tasks = match fs::read_dir(root) {
        Ok(tasks) => tasks,
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => return Ok(0),
        Err(error) => return Err(error),
    };
    let cutoff = max_age.and_then(|age| SystemTime::now().checked_sub(age));
    let mut deleted = 0;
    for task in tasks {
        let task = task?;
        if !task.file_type()?.is_dir() {
            continue;
        }
        let mut files = Vec::new();
        for entry in fs::read_dir(task.path())? {
            let entry = entry?;
            if entry.file_type()?.is_dir() {
                continue;
            }
            let metadata = entry.metadata()?;
            if metadata.is_file() {
                files.push((entry.path(), metadata.modified()?));
            }
        }
        files.sort_by(|a, b| b.1.cmp(&a.1));
        for (index, (path, modified)) in files.iter().enumerate() {
            let too_old = cutoff.is_some_and(|cutoff| *modified < cutoff);
            let too_many = max_per_task > 0 && index >= max_per_task;
            if too_old || too_many {
                match fs::remove_file(path) {
                    Ok(()) => {}
                    Err(error) if error.kind() == std::io::ErrorKind::NotFound => {}
                    Err(error) => return Err(error),
                }
                deleted += 1;
            }
        }
        if fs::read_dir(task.path())?.next().is_none() {
            let _ = fs::remove_dir(task.path());
        }
    }
    Ok(deleted)
}

/// Go `summarize`: the head of a long result, cut back to a rune boundary.
fn summarize(output: &[u8], max: usize) -> Vec<u8> {
    if output.len() <= max {
        return output.to_vec();
    }
    let mut cut = &output[..max];
    while std::str::from_utf8(cut).is_err() {
        cut = &cut[..cut.len() - 1];
    }
    let mut out = cut.to_vec();
    out.extend_from_slice("\n…[externalized — full result via desk_read_result]".as_bytes());
    out
}

// ---------------------------------------------------------------------------
// The loop
// ---------------------------------------------------------------------------

/// The loop's view of one capability.
#[derive(Clone, Debug)]
pub struct AgentTool {
    pub name: String,
    pub description: String,
    pub input_schema: String,
    /// Only read-only tools are ever exposed to the model (issue #317).
    pub read_only: bool,
}

pub struct AgentRun<'a> {
    pub config: &'a Config,
    pub environment: &'a BTreeMap<String, String>,
    pub resolve_key: &'a mut dyn FnMut(&str) -> String,
    pub query: &'a str,
    pub tools: &'a [AgentTool],
    /// Per-vault externalized-results root; `None` degrades to inline output.
    pub results_root: Option<PathBuf>,
    pub cancel: &'a CancelToken,
}

struct Provider {
    kind: Kind,
    url: String,
    model: String,
    max_tokens: i64,
    api_key: String,
}

fn resolve_provider(run: &mut AgentRun<'_>) -> Result<Provider, String> {
    let env = |name: &str| run.environment.get(name).cloned().unwrap_or_default();
    let provider = if run.config.llm_provider.is_empty() {
        "ollama"
    } else {
        run.config.llm_provider.as_str()
    };
    if provider == "anthropic" {
        let api_key = (run.resolve_key)(run.config.api_key_reference());
        if api_key.is_empty() {
            return Err(NOT_CONFIGURED.to_owned());
        }
        let url = env("SYMDESK_ANTHROPIC_URL");
        return Ok(Provider {
            kind: Kind::Anthropic,
            url: if url.is_empty() {
                agent_turn::DEFAULT_ANTHROPIC_URL.to_owned()
            } else {
                url
            },
            model: if run.config.llm_model.is_empty() {
                DEFAULT_ANTHROPIC_MODEL.to_owned()
            } else {
                run.config.llm_model.clone()
            },
            max_tokens: if run.config.max_tokens <= 0 {
                8192
            } else {
                run.config.max_tokens
            },
            api_key,
        });
    }
    let mut base = env("SYMDESK_OLLAMA_URL").trim_end_matches('/').to_owned();
    if base.is_empty() {
        base = run.config.ollama_url.trim_end_matches('/').to_owned();
    }
    if base.is_empty() {
        return Err(NOT_CONFIGURED.to_owned());
    }
    let model = env("SYMDESK_OLLAMA_MODEL");
    Ok(Provider {
        kind: Kind::Ollama,
        url: format!("{base}/api/chat"),
        model: if model.is_empty() {
            DEFAULT_OLLAMA_MODEL.to_owned()
        } else {
            model
        },
        max_tokens: 0,
        api_key: String::new(),
    })
}

fn system_prompt(config: &Config) -> String {
    let mut prompt = String::from(
        "You are the assistant of a local Markdown vault. You can call the provided tools to search, list and read vault content. Call tools whenever you need information; after gathering it, answer the question directly, citing notes as [[path]]. If the vault does not contain the answer, say so honestly.",
    );
    if config.language.is_empty() {
        prompt.push_str(" Answer in the language of the question.\n");
    } else {
        prompt.push_str(&format!(" Answer in {}.\n", config.language));
    }
    prompt
}

/// Runs one tool, capping its output for the next turn. `Err` is the tool's own
/// error text.
fn execute_tool(
    tools: &[AgentTool],
    call: &ToolCall,
    execute: &mut dyn FnMut(&ToolCall) -> Result<String, String>,
) -> Result<(Vec<u8>, bool), String> {
    if !tools.iter().any(|tool| tool.name == call.name) {
        return Err(format!(
            "unknown tool {}",
            symdesk_vault::go_quote(&call.name)
        ));
    }
    let text = execute(call)?;
    let mut output = text.into_bytes();
    if output.len() > MAX_TOOL_OUTPUT {
        output.truncate(MAX_TOOL_OUTPUT);
        output.extend_from_slice("\n…[truncated]".as_bytes());
        return Ok((output, true));
    }
    Ok((output, false))
}

fn sorted(paths: &std::collections::BTreeSet<String>) -> Vec<String> {
    paths.iter().cloned().collect()
}

/// Go `ai.RunAgent`: ask the provider, run the requested read-only tools,
/// append the results and repeat until the model answers without tool calls,
/// the iteration cap is reached or the call is cancelled. Every event goes to
/// `emit`; an `Err` from `emit` (the consumer failed) aborts the run.
///
/// # Errors
///
/// Only the consumer's own failure; provider and tool failures are events.
pub fn run_agent(
    mut run: AgentRun<'_>,
    execute: &mut dyn FnMut(&ToolCall) -> Result<String, String>,
    emit: &mut dyn FnMut(AiEvent) -> Result<(), String>,
) -> Result<(), String> {
    let tools: Vec<AgentTool> = run.tools.iter().filter(|t| t.read_only).cloned().collect();
    if tools.is_empty() {
        // Fallback contract: without tools the loop degrades to one-shot Ask.
        let mut failure = None;
        let mut context = Context::new(run.config, run.environment, &mut *run.resolve_key);
        crate::ai::ask(&mut context, run.query, &[] as &[Doc], &mut |chunk| {
            if failure.is_none() {
                failure = emit(AiEvent::answer(chunk)).err();
            }
        });
        if let Some(error) = failure {
            return Err(error);
        }
        return emit(AiEvent::done());
    }

    let defs: Vec<ToolDef> = tools
        .iter()
        .map(|tool| ToolDef {
            name: tool.name.clone(),
            description: tool.description.clone(),
            input_schema: tool.input_schema.clone(),
        })
        .collect();
    let provider = resolve_provider(&mut run);
    let max_iterations = if run.config.agent_max_iterations <= 0 {
        DEFAULT_MAX_ITERATIONS
    } else {
        run.config.agent_max_iterations
    };

    let externalizer = run
        .results_root
        .clone()
        .map(|root| Externalizer::new(root, new_task_id()));
    if let Some(externalizer) = &externalizer {
        let age_days = u64::try_from(run.config.results_max_age_days).unwrap_or(0);
        let per_task = usize::try_from(run.config.results_max_per_task).unwrap_or(0);
        if age_days > 0 || per_task > 0 {
            // Best effort: a prune failure must never abort the loop.
            let _ = prune_results(
                &externalizer.root,
                (age_days > 0).then(|| Duration::from_secs(age_days * 24 * 3600)),
                per_task,
            );
        }
    }

    let mut messages = vec![Message::User(format!(
        "{}\n\nQuestion: {}",
        system_prompt(run.config),
        run.query
    ))];
    let (mut total_usage, mut context_window) = (0_i64, 0_i64);
    let mut read_paths = std::collections::BTreeSet::new();
    let mut last_warnings: Vec<CitationWarning> = Vec::new();
    let mut iterations = 0_i64;

    while iterations < max_iterations {
        iterations += 1;
        let mut consumer_error = None;
        let turn = match &provider {
            Ok(provider) => agent_turn::run_turn(
                &TurnRequest {
                    kind: provider.kind,
                    url: &provider.url,
                    model: &provider.model,
                    max_tokens: provider.max_tokens,
                    api_key: &provider.api_key,
                    messages: &messages,
                    tools: &defs,
                },
                run.cancel,
                &mut |text| {
                    if !text.is_empty() && consumer_error.is_none() {
                        consumer_error = emit(AiEvent::answer(text)).err();
                    }
                },
            ),
            Err(error) => Err(error.clone()),
        };
        if let Some(error) = consumer_error {
            return Err(error);
        }
        let result = match turn {
            Ok(result) => result,
            Err(error) => {
                emit(AiEvent::answer(&format!(
                    "⚠️ Agent request failed: {error}\n"
                )))?;
                return emit(AiEvent::terminal(total_usage, context_window));
            }
        };
        if result.token_usage > 0 {
            total_usage = result.token_usage;
        }
        if result.context_window > 0 {
            context_window = result.context_window;
        }
        let known = sorted(&read_paths);
        last_warnings = check_citation_warnings(&result.text, &known);

        if result.tool_calls.is_empty() {
            let mut done = AiEvent::terminal(total_usage, context_window);
            done.citation_warnings = last_warnings;
            done.read_paths = known;
            return emit(done);
        }

        messages.push(Message::Assistant {
            text: result.text.clone(),
            tool_calls: result.tool_calls.clone(),
        });
        for call in &result.tool_calls {
            emit(AiEvent::tool_call(iterations, &call.name, &call.input))?;
            let executed = execute_tool(&tools, call, execute);
            let (mut output, mut truncated, failed) = match executed {
                Ok((output, truncated)) => (output, truncated, false),
                Err(error) => (format!("tool error: {error}").into_bytes(), false, true),
            };
            for path in read_paths_for_tool(&call.input, &lossy(&output)) {
                read_paths.insert(path);
            }
            // Externalize over-threshold results (issue #406): the model gets a
            // summary plus the handle; on a write failure the output stays inline.
            if !failed {
                if let Some(externalizer) = &externalizer {
                    if externalizer.should_externalize(&call.name, output.len()) {
                        if let Ok(handle) =
                            externalizer.externalize(&call.name, iterations, &output)
                        {
                            let mut summary = summarize(&output, EXTERNALIZE_PREVIEW);
                            summary.extend_from_slice(
                                format!(
                                    "\n\n[Full result externalized to {handle} — call desk_read_result with handle=\"{handle}\" to read it.]"
                                )
                                .as_bytes(),
                            );
                            output = summary;
                            truncated = false;
                        }
                    }
                }
            }
            emit(AiEvent::tool_result(
                iterations,
                &call.name,
                output.clone(),
                truncated,
            ))?;
            messages.push(Message::Tool {
                call_id: call.id.clone(),
                output,
            });
        }
    }

    emit(AiEvent::answer(
        "\n\n⚠️ **Reached the iteration limit — answer may be partial.**\n",
    ))?;
    let mut done = AiEvent::terminal(total_usage, context_window);
    done.citation_warnings = last_warnings;
    done.read_paths = sorted(&read_paths);
    emit(done)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn warn(content: &str, read: &[&str]) -> Vec<(String, i64)> {
        let read: Vec<String> = read.iter().map(|path| (*path).to_owned()).collect();
        check_citation_warnings(content, &read)
            .into_iter()
            .map(|w| (w.path, w.line))
            .collect()
    }

    #[test]
    fn citation_warnings_cover_sources_sections_tables_and_frontmatter() {
        let content = "---\ntitle: T\nsources:\n  - [[notes/a.md]]\nreference: [[b]]\n---\n\nBody [[ignored.md]] link.\n\n## Quellen\n- [[Interview Anna]]\n- [[read.md#h|alias]]\n- [[plain]]\n\n| Name | Quelle |\n| --- | --- |\n| x | [[Meeting-Notes]] |\n";
        assert_eq!(
            warn(content, &["read.md"]),
            [
                ("notes/a.md".to_owned(), 4),
                ("b".to_owned(), 5),
                ("Interview Anna".to_owned(), 11),
                ("Meeting-Notes".to_owned(), 17),
            ]
        );
        assert!(
            warn(
                content,
                &[
                    "notes/a",
                    "B.md",
                    "interview anna",
                    "meeting-notes",
                    "read.md"
                ]
            )
            .is_empty()
        );
        // Only the first occurrence of a path is reported.
        assert_eq!(warn("## Sources\n- [[a.md]]\n- [[a.md]]\n", &[]).len(), 1);
    }

    #[test]
    fn read_paths_come_from_input_and_output_keys() {
        let paths = read_paths_for_tool(
            r#"{"file":"./notes/a.md","other":"x"}"#,
            r#"{"results":[{"path":"b.md"},{"path":"../escape.md"},{"note":"c.md"}],"path":"http://x"}"#,
        );
        assert_eq!(paths, ["b.md", "c.md", "notes/a.md"]);
    }

    #[test]
    fn go_clean_matches_filepath_clean() {
        for (input, want) in [
            ("a//b/./c/..", "a/b"),
            ("../x", "../x"),
            ("/../x", "/x"),
            ("a/..", "."),
            ("", "."),
        ] {
            assert_eq!(go_clean(input), want);
        }
    }

    #[test]
    fn event_formats_follow_go_json_and_percent_v() {
        let mut event = AiEvent::tool_call(2, "desk_ls", " {\"dir\" : \"a&b\"} ");
        assert_eq!(
            event.json_line().unwrap(),
            r#"{"type":"tool","tool_name":"desk_ls","iteration":2,"tool_inputs":{"dir":"a\u0026b"}}"#
        );
        assert_eq!(
            String::from_utf8(event.go_value_format()).unwrap(),
            "{tool     0 desk_ls  2 [32 123 34 100 105 114 34 32 58 32 34 97 38 98 34 125 32]  false [] [] 0 0}"
        );
        event.tool_inputs = "{\"dir\":".to_owned();
        assert_eq!(
            event.json_line().unwrap_err(),
            "json: error calling MarshalJSON for type json.RawMessage: unexpected end of JSON input"
        );
        let mut done = AiEvent::terminal(60, 200_000);
        done.citation_warnings = vec![CitationWarning {
            path: "n/g.md".into(),
            line: 4,
        }];
        done.read_paths = vec!["a.md".into(), "b.md".into()];
        assert_eq!(
            done.json_line().unwrap(),
            r#"{"type":"done","citation_warnings":[{"path":"n/g.md","line":4}],"read_paths":["a.md","b.md"],"token_usage":60,"context_window":200000}"#
        );
        assert_eq!(
            String::from_utf8(done.go_value_format()).unwrap(),
            "{done     0   0 []  false [{n/g.md 4}] [a.md b.md] 60 200000}"
        );
    }

    #[test]
    fn externalizer_thresholds_skip_set_and_summary_follow_go() {
        let externalizer = Externalizer::new(PathBuf::from("/nowhere"), "task-1".into());
        assert!(externalizer.should_externalize("desk_ls", 8001));
        assert!(!externalizer.should_externalize("desk_ls", 8000));
        assert!(!externalizer.should_externalize("desk_search", 10_000));
        assert!(externalizer.should_externalize("desk_search", 10_001));
        assert!(!externalizer.should_externalize("desk_read_result", 1 << 20));
        assert_eq!(externalizer.handle("desk_ls", 3), "task-1/desk_ls-003.txt");
        let summary = summarize("aé".repeat(3000).as_bytes(), 4000);
        assert!(std::str::from_utf8(&summary).unwrap().ends_with("]"));
        assert_eq!(
            summarize(b"short", 4000),
            b"short".to_vec(),
            "short results stay whole"
        );
    }
}
