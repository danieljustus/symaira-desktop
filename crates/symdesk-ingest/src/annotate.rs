//! Deterministic regex annotation (`internal/ingest/internal/annotate`):
//! built-in profiles, grounded extractions with byte spans and `%q` snippets,
//! and the JSONL sidecar. Go's RE2 classes are ASCII (`\b`, `\d`, `\s`), so the
//! patterns spell those out instead of using Rust's Unicode defaults.

use crate::go_sort::slice_sort_by;
use crate::store_state::{Extraction, Span};
use regex::Regex;
use std::collections::HashSet;
use std::sync::OnceLock;
use symdesk_vault::{go_lowercase, go_quote};

const MAX_SNIPPET_LEN: usize = 120;
/// Directory (below the vault) holding extraction sidecars.
pub const SIDECAR_DIR_NAME: &str = ".symaira/extractions";

pub struct Field {
    pub name: &'static str,
    pub kind: &'static str,
}

/// A built-in extraction profile.
pub struct Profile {
    pub name: &'static str,
    pub fields: &'static [Field],
}

macro_rules! fields {
    ($(($n:expr, $t:expr)),* $(,)?) => { &[$(Field { name: $n, kind: $t }),*] };
}

const GENERIC: Profile = Profile {
    name: "generic",
    fields: fields![
        ("date", "date"),
        ("amount", "amount"),
        ("email", "email"),
        ("url", "url"),
        ("iban", "iban"),
        ("phone", "text")
    ],
};
const INVOICE: Profile = Profile {
    name: "invoice",
    fields: fields![
        ("date", "date"),
        ("amount", "amount"),
        ("invoice_number", "id"),
        ("total", "amount"),
        ("due_date", "deadline"),
        ("email", "email"),
        ("iban", "iban"),
        ("organization", "party")
    ],
};
const CONTRACT: Profile = Profile {
    name: "contract",
    fields: fields![
        ("date", "date"),
        ("deadline", "deadline"),
        ("party", "party"),
        ("email", "email"),
        ("amount", "amount"),
        ("organization", "party")
    ],
};
const JOBCENTER: Profile = Profile {
    name: "jobcenter",
    fields: fields![
        ("date", "date"),
        ("job_id", "id"),
        ("appointment_date", "date"),
        ("deadline", "deadline"),
        ("email", "email"),
        ("organization", "party")
    ],
};

/// The named built-in profile (Go `GetProfile`).
pub fn get_profile(name: &str) -> Result<&'static Profile, String> {
    match name {
        "generic" => Ok(&GENERIC),
        "invoice" => Ok(&INVOICE),
        "contract" => Ok(&CONTRACT),
        "jobcenter" => Ok(&JOBCENTER),
        other => Err(format!("unknown extraction profile {}", go_quote(other))),
    }
}

fn pattern(name: &str) -> Option<&'static Regex> {
    static PATTERNS: OnceLock<Vec<(&'static str, Regex)>> = OnceLock::new();
    let table = PATTERNS.get_or_init(|| {
        // `B` is Go's ASCII word boundary, `D` a digit, `S` ASCII whitespace.
        let compile = |p: &str| {
            let p = p
                .replace("\\b", "(?-u:\\b)")
                .replace("\\d", "[0-9]")
                .replace("\\s", "[\\t\\n\\x0C\\r ]");
            Regex::new(&p).expect("annotate pattern")
        };
        // `\s` inside a bracket class must stay a class member, not a nested class.
        let compile_in_class = |p: &str| {
            let p = p
                .replace("\\b", "(?-u:\\b)")
                .replace("\\d", "[0-9]")
                .replace("[A-Za-z\\s&.-]", "[A-Za-z\\t\\n\\x0C\\r &.-]")
                .replace("[A-Za-z\\s&.,-]", "[A-Za-z\\t\\n\\x0C\\r &.,-]")
                .replace("[\\s#:\\-]", "[\\t\\n\\x0C\\r #:\\-]")
                .replace("[^\\s<>\")\\]]", "[^\\t\\n\\x0C\\r <>\")\\]]")
                .replace("\\s", "[\\t\\n\\x0C\\r ]");
            Regex::new(&p).expect("annotate pattern")
        };
        vec![
            ("date", compile(r"\b\d{4}[-/\.]\d{1,2}[-/\.]\d{1,2}\b|\b\d{1,2}[-/\.]\d{1,2}[-/\.]\d{4}\b|\b\d{1,2}\s+(?:Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec)[a-z]*\s+\d{4}\b")),
            ("amount", compile(r"(?:[$€£¥]\s*[\d.,]+|[\d.,]+\s*(?:USD|EUR|GBP|JPY|CAD|AUD|CHF))\b")),
            ("email", compile(r"\b[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}\b")),
            ("url", compile_in_class(r#"\bhttps?://[^\s<>")\]]+"#)),
            ("iban", compile(r"\b[A-Z]{2}\d{2}[A-Z0-9]{4,30}\b")),
            ("phone", compile(r"(?:\+\d{1,3}[-.\s]?)?\(?\d{2,4}\)?[-.\s]?\d{3,4}[-.\s]?\d{3,4}\b")),
            ("invoice_number", compile_in_class(r"(?i)\b(?:INV(?:OICE)?[\s#:\-]*\d+|Rechnung\s+(?:Nr\.?|No\.?|Nummer)\s*\d+|Faktura\s+#?\d+)\b")),
            ("job_id", compile(r"(?i)\b(?:Case\s*#?\d+|Akte\s+(?:Nr\.?|Nummer)\s*\d+|Fallnummer\s*\d+|Aktenzeichen\s*\d+|Job\s*#?\d+|Vorgang\s*#?\d+)\b")),
            ("organization", compile_in_class(r"\b[A-Z][A-Za-z\s&.-]{2,50}\s+(?:GmbH|Inc\.?|LLC|AG|S\.A\.|Ltd\.?|Corp\.?|Company|Co\.|e\.V\.|Stiftung|Organisation|Organization)\b")),
            ("party", compile_in_class(r"(?i)\b(?:Party|Between|Contractor|Client|Lieferant|Auftraggeber|Auftragnehmer)\s*[:;]?\s+[A-Z][A-Za-z\s&.,-]{2,60}")),
            ("deadline", compile(r"(?i)\b(?:deadline|due\s+date|Frist|Termin|fälligkeit)\s*[:;]?\s*\d{1,2}[-/\.]\d{1,2}[-/\.]\d{4}\b")),
        ]
    });
    table.iter().find(|(n, _)| *n == name).map(|(_, r)| r)
}

/// Go `annotate.Extract`: applies the profile's rules to `text`.
#[must_use]
pub fn extract(profile: &Profile, text: &str) -> Vec<Extraction> {
    let mut seen: HashSet<String> = HashSet::new();
    let mut result: Vec<Extraction> = Vec::new();
    for field in profile.fields {
        let Some(re) = pattern(field.name) else {
            continue;
        };
        for m in re.find_iter(text) {
            let (start, end) = (m.start(), m.end());
            let value = text[start..end].trim().to_owned();
            let key = format!("{}|{}", field.name, go_lowercase(&value));
            if !seen.insert(key) {
                continue;
            }
            result.push(Extraction {
                field: field.name.to_owned(),
                kind: field.kind.to_owned(),
                value,
                span: Some(Span {
                    start: i64::try_from(start).unwrap_or(i64::MAX),
                    end: i64::try_from(end).unwrap_or(i64::MAX),
                    snippet: extract_snippet(text, start, end),
                }),
                matched: true,
            });
        }
    }
    slice_sort_by(&mut result, |a, b| match (&a.span, &b.span) {
        (None, _) => true,
        (_, None) => false,
        (Some(x), Some(y)) => x.start < y.start,
    });
    result
}

/// Go `extractSnippet`, including its `runeEnd = i + 1` quirk.
fn extract_snippet(text: &str, start: usize, end: usize) -> String {
    let runes: Vec<(usize, char)> = text.char_indices().collect();
    let len = runes.len();
    let mut rune_start = 0;
    for (i, (b, _)) in runes.iter().enumerate() {
        if *b >= start {
            rune_start = i;
            break;
        }
    }
    let mut rune_end = len;
    for (i, (b, _)) in runes.iter().enumerate() {
        if *b >= end {
            rune_end = i + 1;
            break;
        }
    }
    let match_len = rune_end as i64 - rune_start as i64;
    let snippet_runes = (MAX_SNIPPET_LEN as i64).max(match_len);
    let half = (snippet_runes - match_len) / 2;
    let mut snippet_start = (rune_start as i64 - half).max(0);
    let mut snippet_end = snippet_start + snippet_runes;
    if snippet_end > len as i64 {
        snippet_end = len as i64;
        snippet_start = (snippet_end - snippet_runes).max(0);
    }
    let body: String = runes[snippet_start as usize..snippet_end as usize]
        .iter()
        .map(|(_, c)| *c)
        .collect();
    let mut snippet = body;
    if snippet_start > 0 {
        snippet = format!("...{snippet}");
    }
    if snippet_end < len as i64 {
        snippet.push_str("...");
    }
    go_quote(&snippet)
}

/// `<vault>/.symaira/extractions/<sha>.jsonl`.
#[must_use]
pub fn sidecar_path(vault: &str, doc_sha256: &str) -> String {
    crate::goos::join(&[vault, SIDECAR_DIR_NAME, &format!("{doc_sha256}.jsonl")])
}

/// Go `json.Marshal` string literal (HTML-safe escaping, `\u00XX` controls).
#[must_use]
pub fn go_json_string(s: &str) -> String {
    let mut out = String::with_capacity(s.len() + 2);
    out.push('"');
    for c in s.chars() {
        match c {
            '"' => out.push_str("\\\""),
            '\\' => out.push_str("\\\\"),
            '\n' => out.push_str("\\n"),
            '\r' => out.push_str("\\r"),
            '\t' => out.push_str("\\t"),
            '<' | '>' | '&' => out.push_str(&format!("\\u{:04x}", c as u32)),
            '\u{2028}' => out.push_str("\\u2028"),
            '\u{2029}' => out.push_str("\\u2029"),
            c if (c as u32) < 0x20 => out.push_str(&format!("\\u{:04x}", c as u32)),
            c => out.push(c),
        }
    }
    out.push('"');
    out
}

/// One JSONL line of Go's `json.Marshal(Extraction)`.
#[must_use]
pub fn extraction_json(e: &Extraction) -> String {
    let mut line = format!(
        "{{\"field\":{},\"type\":{},\"value\":{}",
        go_json_string(&e.field),
        go_json_string(&e.kind),
        go_json_string(&e.value)
    );
    if let Some(span) = &e.span {
        line.push_str(&format!(
            ",\"span\":{{\"start\":{},\"end\":{},\"snippet\":{}}}",
            span.start,
            span.end,
            go_json_string(&span.snippet)
        ));
    }
    line.push_str(&format!(",\"matched\":{}}}", e.matched));
    line
}

/// Writes the sidecar atomically (directory 0700, file 0600).
pub fn write_sidecar(
    vault: &str,
    doc_sha256: &str,
    extractions: &[Extraction],
) -> Result<(), String> {
    let dir = crate::goos::join(&[vault, SIDECAR_DIR_NAME]);
    crate::goos::mkdir_all(&dir, 0o700).map_err(|e| format!("create sidecar directory: {e}"))?;
    let path = crate::goos::join(&[&dir, &format!("{doc_sha256}.jsonl")]);
    let mut data = String::new();
    for e in extractions {
        data.push_str(&extraction_json(e));
        data.push('\n');
    }
    crate::writer::atomic_write_file(&path, data.as_bytes(), 0o600)
        .map_err(|e| format!("write sidecar: {e}"))
}
