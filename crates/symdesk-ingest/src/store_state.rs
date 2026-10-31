//! Remaining Go `store.Store` surface: classification rules, document
//! extractions, mail poll status/cursor and the unscoped/scoped job listings.
//! SQL is copied from Go `store.go`; the oracle is `testdata/port/ingest/store.json`.
use crate::{Job, Result, Store, error, go_timestamp};
use rusqlite::{OptionalExtension, params};
use serde::{Deserialize, Serialize};
use time::OffsetDateTime;

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct ClassificationRule {
    pub id: i64,
    pub pattern: String,
    pub kind: String,
    pub value: String,
    pub created_at: String,
}

/// Mirrors `store.ValidateClassificationRule` (same checks, same order and text).
pub fn validate_classification_rule(pattern: &str, kind: &str, value: &str) -> Result<()> {
    if pattern.is_empty() {
        return Err("pattern cannot be empty".into());
    }
    if !matches!(kind, "category" | "tag" | "correspondent" | "document_type") {
        return Err(format!("invalid rule kind: {}", go_quote(kind)));
    }
    if value.is_empty() {
        return Err("value cannot be empty".into());
    }
    Ok(())
}

/// Go `%q` for the strings this module quotes (valid UTF-8; Rust `str` cannot
/// carry the invalid bytes Go would escape as `\x..`).
fn go_quote(s: &str) -> String {
    let mut out = String::from("\"");
    for c in s.chars() {
        match c {
            '"' => out.push_str("\\\""),
            '\\' => out.push_str("\\\\"),
            '\u{7}' => out.push_str("\\a"),
            '\u{8}' => out.push_str("\\b"),
            '\u{c}' => out.push_str("\\f"),
            '\n' => out.push_str("\\n"),
            '\r' => out.push_str("\\r"),
            '\t' => out.push_str("\\t"),
            '\u{b}' => out.push_str("\\v"),
            c if (c as u32) < 0x20 || c as u32 == 0x7f => {
                out.push_str(&format!("\\x{:02x}", c as u32))
            }
            c => out.push(c),
        }
    }
    out.push('"');
    out
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct Span {
    pub start: i64,
    pub end: i64,
    pub snippet: String,
}

/// `annotate.Extraction`; `span` is omitted when absent, as in Go.
#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct Extraction {
    pub field: String,
    #[serde(rename = "type")]
    pub kind: String,
    pub value: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub span: Option<Span>,
    pub matched: bool,
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct MailPollStatus {
    pub account_id: String,
    /// RFC 3339 with nanoseconds, the form Go's `time.Time` marshals to.
    pub last_polled_at: String,
    pub status: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub last_error: String,
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "PascalCase")]
pub struct MailPollCursor {
    #[serde(rename = "AccountID")]
    pub account_id: String,
    pub folder: String,
    #[serde(rename = "UIDValidity")]
    pub uid_validity: u32,
    #[serde(rename = "LastUID")]
    pub last_uid: u32,
}

/// The text modernc.org/sqlite stores for a `time.Time` parameter
/// (`time.Time.String()` without the monotonic reading):
/// `2006-01-02 15:04:05.999999999 -0700 MST`. Rust has no zone abbreviations, so
/// UTC is written as `+0000 UTC` and any other offset as `+0100 +0100`, which is
/// what Go prints for an unnamed zone.
pub fn go_time_text(at: OffsetDateTime) -> String {
    let nanos = at.nanosecond();
    let mut out = format!(
        "{:04}-{:02}-{:02} {:02}:{:02}:{:02}",
        at.year(),
        u8::from(at.month()),
        at.day(),
        at.hour(),
        at.minute(),
        at.second()
    );
    if nanos != 0 {
        let frac = format!("{nanos:09}");
        out.push('.');
        out.push_str(frac.trim_end_matches('0'));
    }
    let offset = at.offset().whole_seconds();
    let (sign, abs) = if offset < 0 {
        ('-', -offset)
    } else {
        ('+', offset)
    };
    let zone = format!("{sign}{:02}{:02}", abs / 3600, abs % 3600 / 60);
    if offset == 0 {
        out.push_str(" +0000 UTC");
    } else {
        out.push_str(&format!(" {zone} {zone}"));
    }
    out
}

/// Parses the stored text back to Go's JSON form (`RFC3339Nano`). Accepts the
/// `time.Time.String()` form (with an optional `m=±…` monotonic suffix and any
/// zone abbreviation) and the plain SQLite `YYYY-MM-DD HH:MM:SS` form.
fn go_time_json(text: &str) -> Result<String> {
    let text = text.split(" m=").next().unwrap_or(text);
    let mut parts = text.split(' ');
    let date = parts.next().ok_or("empty time")?;
    let clock = parts.next().ok_or_else(|| format!("bad time {text:?}"))?;
    let offset = parts.next().unwrap_or("+0000");
    let (clock, frac) = clock.split_once('.').unwrap_or((clock, ""));
    let (sign, digits) = match offset.as_bytes().first() {
        Some(b'-') => (-1, &offset[1..]),
        Some(b'+') => (1, &offset[1..]),
        _ => (1, "0000"),
    };
    if digits.len() != 4 || !digits.bytes().all(|b| b.is_ascii_digit()) {
        return Err(format!("bad time zone in {text:?}"));
    }
    let off_s: i32 = sign
        * (digits[..2].parse::<i32>().map_err(|e| e.to_string())? * 3600
            + digits[2..].parse::<i32>().map_err(|e| e.to_string())? * 60);
    let mut out = format!("{date}T{clock}");
    if !frac.is_empty() {
        out.push('.');
        out.push_str(frac.trim_end_matches('0'));
        if out.ends_with('.') {
            out.pop();
        }
    }
    if off_s == 0 {
        out.push('Z');
    } else {
        let a = off_s.abs();
        out.push_str(&format!(
            "{}{:02}:{:02}",
            if off_s < 0 { '-' } else { '+' },
            a / 3600,
            a % 3600 / 60
        ));
    }
    Ok(out)
}

impl Store {
    pub fn add_rule(&self, pattern: &str, kind: &str, value: &str) -> Result<ClassificationRule> {
        validate_classification_rule(pattern, kind, value)?;
        self.connection
            .execute(
                "INSERT INTO classification_rules (pattern, kind, value) VALUES (?, ?, ?)",
                params![pattern, kind, value],
            )
            .map_err(|e| error("insert rule", e))?;
        let id = self.connection.last_insert_rowid();
        let created: String = self
            .connection
            .query_row(
                "SELECT created_at FROM classification_rules WHERE id = ?",
                [id],
                |r| r.get(0),
            )
            .map_err(|e| error("get rule created_at", e))?;
        Ok(ClassificationRule {
            id,
            pattern: pattern.into(),
            kind: kind.into(),
            value: value.into(),
            created_at: go_timestamp(created),
        })
    }

    pub fn list_rules(&self) -> Result<Option<Vec<ClassificationRule>>> {
        let mut stmt = self
            .connection
            .prepare(
                "SELECT id, pattern, kind, value, created_at FROM classification_rules ORDER BY id ASC",
            )
            .map_err(|e| error("query rules", e))?;
        let rules = stmt
            .query_map([], |r| {
                Ok(ClassificationRule {
                    id: r.get(0)?,
                    pattern: r.get(1)?,
                    kind: r.get(2)?,
                    value: r.get(3)?,
                    created_at: go_timestamp(r.get(4)?),
                })
            })
            .map_err(|e| error("query rules", e))?
            .collect::<std::result::Result<Vec<_>, _>>()
            .map_err(|e| error("scan rule", e))?;
        Ok(if rules.is_empty() { None } else { Some(rules) })
    }

    pub fn update_rule(
        &self,
        id: i64,
        pattern: &str,
        kind: &str,
        value: &str,
    ) -> Result<ClassificationRule> {
        validate_classification_rule(pattern, kind, value)?;
        let changed = self
            .connection
            .execute(
                "UPDATE classification_rules SET pattern = ?, kind = ?, value = ? WHERE id = ?",
                params![pattern, kind, value, id],
            )
            .map_err(|e| error("update rule", e))?;
        if changed == 0 {
            return Err(format!("rule not found with ID {id}"));
        }
        self.connection
            .query_row(
                "SELECT id, pattern, kind, value, created_at FROM classification_rules WHERE id = ?",
                [id],
                |r| {
                    Ok(ClassificationRule {
                        id: r.get(0)?,
                        pattern: r.get(1)?,
                        kind: r.get(2)?,
                        value: r.get(3)?,
                        created_at: go_timestamp(r.get(4)?),
                    })
                },
            )
            .map_err(|e| error("get updated rule", e))
    }

    pub fn delete_rule(&self, id: i64) -> Result<()> {
        let changed = self
            .connection
            .execute("DELETE FROM classification_rules WHERE id = ?", [id])
            .map_err(|e| error("delete rule", e))?;
        if changed == 0 {
            return Err(format!("rule not found with ID {id}"));
        }
        Ok(())
    }

    /// Like Go, extractions are inserted one by one without a transaction: the
    /// rows before a failing one stay.
    pub fn record_extractions(
        &self,
        document_id: i64,
        profile: &str,
        extractions: &[Extraction],
    ) -> Result<()> {
        for e in extractions {
            let (start, end, snippet) = match &e.span {
                Some(s) => (s.start, s.end, s.snippet.as_str()),
                None => (0, 0, ""),
            };
            self.connection
                .execute(
                    "INSERT INTO document_extractions (document_id, profile, field_type, value, start_offset, end_offset, snippet)
			 VALUES (?, ?, ?, ?, ?, ?, ?)",
                    params![document_id, profile, e.field, e.value, start, end, snippet],
                )
                .map_err(|e| error("record extraction", e))?;
        }
        Ok(())
    }

    pub fn list_extractions(&self, document_id: i64) -> Result<Option<Vec<Extraction>>> {
        let mut stmt = self
            .connection
            .prepare(
                "SELECT field_type, value, start_offset, end_offset, snippet
		 FROM document_extractions
		 WHERE document_id = ?
		 ORDER BY id ASC",
            )
            .map_err(|e| error("query extractions", e))?;
        let rows = stmt
            .query_map([document_id], |r| {
                let field: String = r.get(0)?;
                let (start, end, snippet): (i64, i64, String) = (r.get(2)?, r.get(3)?, r.get(4)?);
                Ok(Extraction {
                    kind: field.clone(),
                    field,
                    value: r.get(1)?,
                    span: (start != 0 || end != 0 || !snippet.is_empty()).then_some(Span {
                        start,
                        end,
                        snippet,
                    }),
                    matched: true,
                })
            })
            .map_err(|e| error("query extractions", e))?
            .collect::<std::result::Result<Vec<_>, _>>()
            .map_err(|e| error("scan extraction", e))?;
        Ok(if rows.is_empty() { None } else { Some(rows) })
    }

    pub fn record_mail_poll_status(
        &self,
        account_id: &str,
        polled_at: OffsetDateTime,
        status: &str,
        last_error: &str,
    ) -> Result<()> {
        self.connection
            .execute(
                "INSERT INTO mail_poll_status (account_id, last_polled_at, status, last_error) VALUES (?, ?, ?, ?)
		 ON CONFLICT(account_id) DO UPDATE SET last_polled_at = excluded.last_polled_at, status = excluded.status, last_error = excluded.last_error",
                params![account_id, go_time_text(polled_at), status, last_error],
            )
            .map_err(|e| error("", e))?;
        Ok(())
    }

    pub fn get_mail_poll_status(&self, account_id: &str) -> Result<Option<MailPollStatus>> {
        let row = self
            .connection
            .query_row(
                "SELECT account_id, last_polled_at, status, last_error FROM mail_poll_status WHERE account_id = ?",
                [account_id],
                |r| {
                    Ok((
                        r.get::<_, String>(0)?,
                        r.get::<_, String>(1)?,
                        r.get::<_, String>(2)?,
                        r.get::<_, Option<String>>(3)?,
                    ))
                },
            )
            .optional()
            .map_err(|e| error("", e))?;
        row.map(|(account_id, at, status, last_error)| {
            Ok(MailPollStatus {
                account_id,
                last_polled_at: go_time_json(&at)?,
                status,
                last_error: last_error.unwrap_or_default(),
            })
        })
        .transpose()
    }

    pub fn get_mail_poll_cursor(&self, account_id: &str) -> Result<Option<MailPollCursor>> {
        self.connection
            .query_row(
                "SELECT account_id, folder, uid_validity, last_uid FROM mail_poll_cursor WHERE account_id = ?",
                [account_id],
                |r| {
                    Ok(MailPollCursor {
                        account_id: r.get(0)?,
                        folder: r.get(1)?,
                        uid_validity: r.get(2)?,
                        last_uid: r.get(3)?,
                    })
                },
            )
            .optional()
            .map_err(|e| error("", e))
    }

    pub fn set_mail_poll_cursor(
        &self,
        account_id: &str,
        folder: &str,
        uid_validity: u32,
        last_uid: u32,
    ) -> Result<()> {
        self.connection
            .execute(
                "INSERT INTO mail_poll_cursor (account_id, folder, uid_validity, last_uid) VALUES (?, ?, ?, ?)
		 ON CONFLICT(account_id) DO UPDATE SET folder = excluded.folder, uid_validity = excluded.uid_validity, last_uid = excluded.last_uid",
                params![account_id, folder, uid_validity, last_uid],
            )
            .map_err(|e| error("", e))?;
        Ok(())
    }

    /// Go `ListJobs`: the unscoped first page.
    pub fn list_jobs(&self, limit: i64) -> Result<Option<Vec<Job>>> {
        Ok(self.list_jobs_page("", limit, 0)?.0)
    }

    /// Go `ListJobsInVault`: the explicit scoped form of `list_jobs_page`.
    pub fn list_jobs_in_vault(
        &self,
        vault: &str,
        limit: i64,
        offset: i64,
    ) -> Result<(Option<Vec<Job>>, i64)> {
        self.list_jobs_page(vault, limit, offset)
    }
}
