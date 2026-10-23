//! CSV text extraction for retrieval re-embedding.
//!
//! Go's `parser.ParseFileSections` routes `.csv` through `parseCSV`: an
//! `encoding/csv` reader with `FieldsPerRecord = -1`, every record's fields
//! joined with tabs and terminated by a newline, the whole text trimmed, and
//! the **first read error silently ending the file** (rows before a bare
//! quote or an unterminated quoted field are kept, the failing record and
//! everything after it are dropped). This module ports the Go reader's
//! `readRecord`/`readLine` byte semantics so those quirks stay identical.

use std::borrow::Cow;

use crate::retrieval_markdown::MAX_RETRIEVAL_SOURCE_BYTES;
use crate::{RetrievalAnchor, RetrievalSection, SidecarError};

/// The two switches of Go's `csv.Reader` that change which rows survive.
/// Production always uses [`GO_DIALECT`]; the flipped values exist only so the
/// differential can prove each quirk is load-bearing.
#[derive(Clone, Copy)]
struct Dialect {
    lazy_quotes: bool,
    keep_failed_record: bool,
}

const GO_DIALECT: Dialect = Dialect {
    lazy_quotes: false,
    keep_failed_record: false,
};

enum Step {
    Record(Vec<Vec<u8>>),
    /// Go returned a `ParseError` together with the fields read so far.
    Failed(Vec<Vec<u8>>),
    Eof,
}

struct Reader<'a> {
    data: &'a [u8],
    position: usize,
    dialect: Dialect,
}

fn length_nl(bytes: &[u8]) -> usize {
    usize::from(bytes.last() == Some(&b'\n'))
}

impl<'a> Reader<'a> {
    /// Go `readLine`: the next line with its `\n`, `\r\n` folded to `\n`, a
    /// final unterminated line accepted with a trailing `\r` dropped. The flag
    /// is true only for a clean end of input (no bytes read).
    fn read_line(&mut self) -> (Cow<'a, [u8]>, bool) {
        let remaining = &self.data[self.position..];
        if remaining.is_empty() {
            return (Cow::Borrowed(&[]), true);
        }
        let (mut line, terminated) = match remaining.iter().position(|byte| *byte == b'\n') {
            Some(index) => (&remaining[..=index], true),
            None => (remaining, false),
        };
        self.position += line.len();
        if !terminated && line.last() == Some(&b'\r') {
            line = &line[..line.len() - 1];
        }
        if line.len() >= 2 && line[line.len() - 2] == b'\r' && line[line.len() - 1] == b'\n' {
            let mut folded = line[..line.len() - 2].to_vec();
            folded.push(b'\n');
            return (Cow::Owned(folded), false);
        }
        (Cow::Borrowed(line), false)
    }

    /// Go `readRecord` with `Comma = ','`, no comment character and
    /// `TrimLeadingSpace = false`.
    fn read_record(&mut self) -> Step {
        let lazy = self.dialect.lazy_quotes;
        let mut line;
        loop {
            let (next, eof) = self.read_line();
            line = next;
            if eof {
                return Step::Eof;
            }
            if line.len() == length_nl(&line) {
                continue;
            }
            break;
        }
        let mut at = 0usize;
        let mut fields: Vec<Vec<u8>> = Vec::new();
        let mut failed = false;
        'field: loop {
            let rest = &line[at..];
            if rest.first() != Some(&b'"') {
                let comma = rest.iter().position(|byte| *byte == b',');
                let field = match comma {
                    Some(index) => &rest[..index],
                    None => &rest[..rest.len() - length_nl(rest)],
                };
                if !lazy && field.contains(&b'"') {
                    failed = true;
                    break 'field;
                }
                fields.push(field.to_vec());
                match comma {
                    Some(index) => {
                        at += index + 1;
                        continue 'field;
                    }
                    None => break 'field,
                }
            }
            at += 1;
            let mut current: Vec<u8> = Vec::new();
            loop {
                let rest = &line[at..];
                if let Some(index) = rest.iter().position(|byte| *byte == b'"') {
                    current.extend_from_slice(&rest[..index]);
                    at += index + 1;
                    let rest = &line[at..];
                    if rest.first() == Some(&b'"') {
                        current.push(b'"');
                        at += 1;
                    } else if rest.first() == Some(&b',') {
                        at += 1;
                        fields.push(current);
                        continue 'field;
                    } else if length_nl(rest) == rest.len() {
                        fields.push(current);
                        break 'field;
                    } else if lazy {
                        current.push(b'"');
                    } else {
                        failed = true;
                        break 'field;
                    }
                } else if !rest.is_empty() {
                    current.extend_from_slice(rest);
                    let (next, _eof) = self.read_line();
                    line = next;
                    at = 0;
                } else {
                    // Abrupt end of input inside a quoted field.
                    if lazy {
                        fields.push(current);
                    } else {
                        failed = true;
                    }
                    break 'field;
                }
            }
        }
        if failed {
            Step::Failed(fields)
        } else {
            Step::Record(fields)
        }
    }
}

fn csv_text(content: &[u8], dialect: Dialect) -> Vec<u8> {
    let mut reader = Reader {
        data: content,
        position: 0,
        dialect,
    };
    let mut text = Vec::new();
    let push_record = |text: &mut Vec<u8>, fields: &[Vec<u8>]| {
        for (index, field) in fields.iter().enumerate() {
            if index > 0 {
                text.push(b'\t');
            }
            text.extend_from_slice(field);
        }
        text.push(b'\n');
    };
    loop {
        match reader.read_record() {
            Step::Record(fields) => push_record(&mut text, &fields),
            Step::Failed(fields) => {
                if dialect.keep_failed_record {
                    push_record(&mut text, &fields);
                }
                break;
            }
            Step::Eof => break,
        }
    }
    text
}

fn parse_with(
    source_path: &str,
    content: &[u8],
    dialect: Dialect,
) -> Result<Vec<RetrievalSection>, SidecarError> {
    if content.len() > MAX_RETRIEVAL_SOURCE_BYTES {
        return Err(SidecarError::Contract(format!(
            "file {source_path} exceeds {MAX_RETRIEVAL_SOURCE_BYTES} byte limit ({} bytes)",
            content.len()
        )));
    }
    let bytes = csv_text(content, dialect);
    // Go keeps invalid UTF-8 bytes in the section; retrieval sections are Rust
    // strings, so such a file is rejected instead of being stored lossily.
    let text = std::str::from_utf8(&bytes).map_err(|error| {
        SidecarError::Contract(format!(
            "file {source_path} must be valid UTF-8 (invalid byte at {})",
            error.valid_up_to()
        ))
    })?;
    let text = text.trim();
    if text.is_empty() {
        return Ok(Vec::new());
    }
    Ok(vec![RetrievalSection {
        text: text.to_owned(),
        start: 0,
        anchor: RetrievalAnchor {
            kind: "text".to_owned(),
            value: "offset:0".to_owned(),
        },
        synthetic: false,
    }])
}

/// Parses a `.csv` source the way Go's `ParseFileSections` does: one
/// offset-zero text section holding the tab-joined rows, or no section when
/// the extracted text is blank.
pub fn parse_csv_retrieval_sections(
    source_path: &str,
    content: &[u8],
) -> Result<Vec<RetrievalSection>, SidecarError> {
    parse_with(source_path, content, GO_DIALECT)
}

#[cfg(test)]
mod tests {
    use super::{Dialect, GO_DIALECT, MAX_RETRIEVAL_SOURCE_BYTES, csv_text, parse_with};
    use crate::{RetrievalSection, SidecarError};
    use base64::{Engine, engine::general_purpose::STANDARD};
    use serde::Deserialize;

    #[derive(Deserialize)]
    struct Fixture {
        schema_version: u32,
        max_file_bytes: usize,
        cases: Vec<Case>,
    }

    #[derive(Clone, Deserialize)]
    struct Case {
        id: String,
        input_base64: String,
        sections: Vec<Expected>,
        #[serde(default)]
        error: String,
        #[serde(default)]
        size_limit: bool,
    }

    #[derive(Clone, Deserialize)]
    struct Expected {
        text_base64: String,
        valid_utf8: bool,
        start: usize,
        anchor_kind: String,
        anchor_value: String,
        synthetic: bool,
    }

    fn fixture() -> Fixture {
        let path = std::path::Path::new(env!("CARGO_MANIFEST_DIR"))
            .join("../../testdata/port/retrieval/csv-sections.json");
        let fixture: Fixture =
            serde_json::from_slice(&std::fs::read(path).expect("Go-generated CSV fixture"))
                .expect("decode CSV fixture");
        assert_eq!(fixture.schema_version, 1);
        assert_eq!(fixture.max_file_bytes, MAX_RETRIEVAL_SOURCE_BYTES);
        fixture
    }

    fn input(case: &Case) -> Vec<u8> {
        if case.size_limit {
            vec![b'a'; MAX_RETRIEVAL_SOURCE_BYTES + 1]
        } else {
            STANDARD
                .decode(&case.input_base64)
                .expect("decode case input")
        }
    }

    fn describe(result: &Result<Vec<RetrievalSection>, String>) -> String {
        match result {
            Ok(sections) => format!("{sections:?}"),
            Err(error) => format!("error: {error}"),
        }
    }

    /// Returns one entry per case whose result differs from Go's.
    fn mismatches(fixture: &Fixture, parse: &ParseFn) -> Vec<String> {
        let mut drift = Vec::new();
        for case in &fixture.cases {
            let actual = parse("$PATH", &input(case)).map_err(|error| match error {
                SidecarError::Contract(message) => message,
                other => other.to_string(),
            });
            let invalid_utf8 = case.sections.iter().any(|section| !section.valid_utf8);
            if invalid_utf8 {
                // Documented residual: Go keeps the raw bytes, Rust rejects.
                match &actual {
                    Err(message) if message.contains("must be valid UTF-8") => {}
                    other => drift.push(format!("{}: {}", case.id, describe(other))),
                }
                continue;
            }
            let expected: Result<Vec<RetrievalSection>, String> = if case.error.is_empty() {
                Ok(case
                    .sections
                    .iter()
                    .map(|section| RetrievalSection {
                        text: String::from_utf8(
                            STANDARD.decode(&section.text_base64).expect("decode text"),
                        )
                        .expect("valid UTF-8 expected text"),
                        start: section.start,
                        anchor: crate::RetrievalAnchor {
                            kind: section.anchor_kind.clone(),
                            value: section.anchor_value.clone(),
                        },
                        synthetic: section.synthetic,
                    })
                    .collect())
            } else {
                Err(case.error.clone())
            };
            if actual != expected {
                drift.push(format!(
                    "{}: expected {} got {}",
                    case.id,
                    describe(&expected),
                    describe(&actual)
                ));
            }
        }
        drift
    }

    type ParseFn = dyn Fn(&str, &[u8]) -> Result<Vec<RetrievalSection>, SidecarError>;

    fn production(path: &str, bytes: &[u8]) -> Result<Vec<RetrievalSection>, SidecarError> {
        parse_with(path, bytes, GO_DIALECT)
    }

    #[test]
    fn retrieval_csv_sections_replay_go_parse_file_sections() {
        let fixture = fixture();
        assert_eq!(fixture.cases.len(), 35, "every Go case must execute");
        assert!(
            fixture.cases.iter().any(|case| !case.sections.is_empty()),
            "fixture must contain extracted text"
        );
        assert!(
            fixture.cases.iter().any(|case| case.sections.is_empty()),
            "fixture must contain blank results"
        );
        assert_eq!(
            fixture.cases.iter().filter(|case| case.size_limit).count(),
            1
        );
        let drift = mismatches(&fixture, &production);
        assert!(drift.is_empty(), "drift from Go: {drift:#?}");
    }

    #[test]
    fn retrieval_csv_mutated_dialects_are_rejected_by_the_fixture() {
        let fixture = fixture();
        let lazy = |path: &str, bytes: &[u8]| {
            parse_with(
                path,
                bytes,
                Dialect {
                    lazy_quotes: true,
                    ..GO_DIALECT
                },
            )
        };
        let keep = |path: &str, bytes: &[u8]| {
            parse_with(
                path,
                bytes,
                Dialect {
                    keep_failed_record: true,
                    ..GO_DIALECT
                },
            )
        };
        let naive = |path: &str, bytes: &[u8]| {
            // Wrong on purpose: no quoting, no blank-line skipping.
            let text = String::from_utf8_lossy(bytes).replace("\r\n", "\n");
            let joined = text
                .lines()
                .collect::<Vec<_>>()
                .join("\n")
                .replace(',', "\t");
            let _ = path;
            Ok(if joined.trim().is_empty() {
                Vec::new()
            } else {
                vec![RetrievalSection {
                    text: joined.trim().to_owned(),
                    start: 0,
                    anchor: crate::RetrievalAnchor {
                        kind: "text".to_owned(),
                        value: "offset:0".to_owned(),
                    },
                    synthetic: false,
                }]
            })
        };
        let lazy_drift = mismatches(&fixture, &lazy);
        let keep_drift = mismatches(&fixture, &keep);
        let naive_drift = mismatches(&fixture, &naive);
        for (name, drift, must_fail) in [
            ("lazy quotes", &lazy_drift, "bare-quote-keeps-prior-rows"),
            (
                "keep failed record",
                &keep_drift,
                "unterminated-quote-no-newline",
            ),
            ("naive split", &naive_drift, "quoted-comma"),
        ] {
            assert!(
                drift.iter().any(|entry| entry.starts_with(must_fail)),
                "{name} mutant must fail {must_fail}: {drift:#?}"
            );
        }
        assert!(
            naive_drift.len() >= 5,
            "naive mutant drift: {naive_drift:#?}"
        );
    }

    #[test]
    fn retrieval_csv_comparator_rejects_a_tampered_expectation() {
        let mut fixture = fixture();
        let target = fixture
            .cases
            .iter_mut()
            .find(|case| case.id == "simple-lf")
            .expect("simple-lf case");
        target.sections[0].text_base64 = STANDARD.encode("a\tb\n1\t3");
        let drift = mismatches(&fixture, &production);
        assert_eq!(drift.len(), 1, "{drift:#?}");
        assert!(drift[0].starts_with("simple-lf"));
    }

    #[test]
    fn retrieval_csv_stops_at_first_error_and_keeps_prior_rows() {
        assert_eq!(
            csv_text(b"a,b\nc\"d,e\nf,g\n", GO_DIALECT),
            b"a\tb\n".to_vec()
        );
        assert_eq!(csv_text(b"\"a\"\r\n\r\nb", GO_DIALECT), b"a\nb\n".to_vec());
    }
}
