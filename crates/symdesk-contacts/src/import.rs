//! CSV identity parsing and the import domain's language-neutral shapes.
use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};

#[derive(Clone, Debug, Default, Deserialize, Serialize, PartialEq, Eq)]
#[serde(rename_all = "PascalCase")]
pub struct ColumnMapping {
    pub display_name: String,
    pub given_name: String,
    pub family_name: String,
    pub organization: String,
    pub title: String,
    pub email: String,
    pub phone: String,
    #[serde(rename = "URL")]
    pub url: String,
}
#[derive(Clone, Debug, Default, Deserialize, Serialize, PartialEq, Eq)]
#[serde(rename_all = "PascalCase")]
pub struct ImportRow {
    pub row_number: i64,
    pub display_name: String,
    pub given_name: String,
    pub family_name: String,
    pub organization_name: String,
    pub title: String,
    pub emails: Option<Vec<String>>,
    pub phones: Option<Vec<String>>,
    #[serde(rename = "URLs")]
    pub urls: Option<Vec<String>>,
    pub source_ref: String,
}
#[derive(Clone, Debug, Default, Deserialize, Serialize, PartialEq, Eq)]
#[serde(rename_all = "PascalCase")]
pub struct ValidationIssue {
    pub row_number: i64,
    pub field: String,
    pub message: String,
}

pub fn detect_column_mapping(header: &[String]) -> ColumnMapping {
    let lower: std::collections::HashMap<_, _> = header
        .iter()
        .map(|h| (crate::pipeline::go_lower(h.trim()), h.clone()))
        .collect();
    let find = |aliases: &[&str]| {
        aliases
            .iter()
            .find_map(|a| lower.get(*a))
            .cloned()
            .unwrap_or_default()
    };
    ColumnMapping {
        display_name: find(&["name", "full name", "display name"]),
        given_name: find(&["given name", "first name", "firstname"]),
        family_name: find(&["family name", "last name", "lastname", "surname"]),
        organization: find(&["organization", "org", "company"]),
        title: find(&["title", "job title"]),
        email: find(&["email", "e-mail", "email address"]),
        phone: find(&["phone", "telephone", "phone number", "mobile"]),
        url: find(&["url", "website", "web"]),
    }
}

pub type ParseResult = Result<(Option<Vec<ImportRow>>, Option<Vec<ValidationIssue>>), String>;

/// Match encoding/csv's strict quote handling, unrestricted record width, blank
/// line skipping and byte-indexed error positions. A BOM is intentionally *not*
/// removed: Go does not strip it from headers.
pub fn parse_csv(input: &str, mapping: &ColumnMapping) -> ParseResult {
    let mut reader = CsvReader::new(input);
    let header = reader
        .read()
        .ok_or("csv file is empty")?
        .map_err(|e| format!("failed to read CSV header: {e}"))?;
    let col: std::collections::HashMap<_, _> = header
        .iter()
        .enumerate()
        .map(|(i, h)| (h.as_str(), i))
        .collect();
    let mut rows = Vec::new();
    let mut issues = Vec::new();
    let mut row_number = 0;
    while let Some(record) = reader.read() {
        row_number += 1;
        let record = match record {
            Ok(r) => r,
            Err(message) => {
                issues.push(ValidationIssue {
                    row_number,
                    field: String::new(),
                    message,
                });
                continue;
            }
        };
        let field = |h: &str| {
            col.get(h)
                .filter(|_| !h.is_empty())
                .and_then(|i| record.get(*i))
                .map(|v| v.trim().to_owned())
                .unwrap_or_default()
        };
        let multi = |h: &str| {
            let s = field(h);
            if s.is_empty() {
                None
            } else {
                Some(
                    s.split(';')
                        .map(str::trim)
                        .filter(|v| !v.is_empty())
                        .map(str::to_owned)
                        .collect(),
                )
            }
        };
        let mut row = ImportRow {
            row_number,
            display_name: field(&mapping.display_name),
            given_name: field(&mapping.given_name),
            family_name: field(&mapping.family_name),
            organization_name: field(&mapping.organization),
            title: field(&mapping.title),
            emails: multi(&mapping.email),
            phones: multi(&mapping.phone),
            urls: multi(&mapping.url),
            ..ImportRow::default()
        };
        if row.display_name.is_empty() {
            row.display_name = format!("{} {}", row.given_name, row.family_name)
                .trim()
                .to_owned();
        }
        if row.display_name.is_empty() {
            issues.push(ValidationIssue {
                row_number,
                field: "name".into(),
                message: "row has no display name, given name or family name".into(),
            });
            continue;
        }
        row.source_ref = identity_hash("csv:", &row);
        rows.push(row);
    }
    Ok((
        (!rows.is_empty()).then_some(rows),
        (!issues.is_empty()).then_some(issues),
    ))
}

pub fn identity_hash(prefix: &str, row: &ImportRow) -> String {
    let value = format!(
        "{}|{}|{}|{}",
        row.display_name,
        row.given_name,
        row.family_name,
        row.emails.as_deref().unwrap_or_default().join(",")
    );
    format!(
        "{prefix}{:.16}",
        format!("{:x}", Sha256::digest(value.as_bytes()))
    )
}

struct CsvReader {
    lines: Vec<Vec<u8>>,
    next: usize,
}
impl CsvReader {
    fn new(input: &str) -> Self {
        let lines = input
            .as_bytes()
            .split_inclusive(|b| *b == b'\n')
            .map(|line| {
                let mut line = line.to_vec();
                if line.ends_with(b"\r\n") {
                    line.remove(line.len() - 2);
                } else if line.ends_with(b"\r") {
                    line.pop();
                }
                line
            })
            .collect();
        Self { lines, next: 0 }
    }
    fn read(&mut self) -> Option<Result<Vec<String>, String>> {
        while self
            .lines
            .get(self.next)
            .is_some_and(|l| l == b"\n" || l.is_empty())
        {
            self.next += 1;
        }
        let mut line = self.lines.get(self.next)?.clone();
        self.next += 1;
        let start = self.next;
        let mut line_num = start;
        let mut pos = 0;
        let mut record = Vec::new();
        let error = |line_num: usize, col: usize, msg: &str| {
            if line_num == start {
                format!("parse error on line {line_num}, column {col}: {msg}")
            } else {
                format!(
                    "record on line {start}; parse error on line {line_num}, column {col}: {msg}"
                )
            }
        };
        loop {
            let mut field = Vec::new();
            if line.get(pos) == Some(&b'"') {
                pos += 1;
                loop {
                    if pos == line.len() {
                        if let Some(next) = self.lines.get(self.next) {
                            line = next.clone();
                            self.next += 1;
                            line_num += 1;
                            pos = 0;
                            continue;
                        } else {
                            return Some(Err(error(
                                line_num,
                                pos + 1,
                                "extraneous or missing \" in quoted-field",
                            )));
                        }
                    }
                    match line[pos] {
                        b'"' => {
                            pos += 1;
                            match line.get(pos) {
                                Some(b'"') => {
                                    field.push(b'"');
                                    pos += 1;
                                }
                                Some(b',') => {
                                    pos += 1;
                                    record.push(String::from_utf8(field).unwrap());
                                    break;
                                }
                                Some(b'\n') | None => {
                                    record.push(String::from_utf8(field).unwrap());
                                    return Some(Ok(record));
                                }
                                _ => {
                                    return Some(Err(error(
                                        line_num,
                                        pos,
                                        "extraneous or missing \" in quoted-field",
                                    )));
                                }
                            }
                        }
                        b => {
                            field.push(b);
                            pos += 1;
                        }
                    }
                }
            } else {
                while let Some(&b) = line.get(pos) {
                    if b == b',' || b == b'\n' {
                        break;
                    }
                    if b == b'"' {
                        return Some(Err(error(line_num, pos + 1, "bare \" in non-quoted-field")));
                    }
                    field.push(b);
                    pos += 1;
                }
                record.push(String::from_utf8(field).unwrap());
                if line.get(pos) == Some(&b',') {
                    pos += 1;
                } else {
                    return Some(Ok(record));
                }
            }
        }
    }
}
