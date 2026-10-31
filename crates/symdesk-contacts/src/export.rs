//! Data-minimized JSON/CSV export. Notes and audit metadata are never exported.
//! The JSON writer reproduces Go's `encoding/json` indentation and string
//! escaping (HTML-safe `\u003c`/`\u003e`/`\u0026`, `\u2028`/`\u2029`), and the
//! CSV writer reproduces `encoding/csv` quoting.
use crate::contact::{Error, error};
use crate::security::Service;
use rusqlite::types::ValueRef;
use std::collections::BTreeMap;
use std::io::Write;

#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct ExportPerson {
    pub id: String,
    pub display_name: String,
    pub given_name: String,
    pub family_name: String,
    pub emails: Vec<String>,
    pub phones: Vec<String>,
    pub tags: Vec<String>,
}

fn text(row: &rusqlite::Row<'_>, index: usize, name: &str) -> Result<String, String> {
    match row.get_ref(index).map_err(|e| e.to_string())? {
        ValueRef::Null => Err(format!(
            "sql: Scan error on column index {index}, name \"{name}\": converting NULL to string is unsupported"
        )),
        ValueRef::Text(bytes) => Ok(String::from_utf8_lossy(bytes).into_owned()),
        ValueRef::Integer(n) => Ok(n.to_string()),
        ValueRef::Real(f) => Ok(f.to_string()),
        ValueRef::Blob(bytes) => Ok(String::from_utf8_lossy(bytes).into_owned()),
    }
}

fn json_string(out: &mut String, s: &str) {
    out.push('"');
    for c in s.chars() {
        match c {
            '"' => out.push_str("\\\""),
            '\\' => out.push_str("\\\\"),
            '\n' => out.push_str("\\n"),
            '\r' => out.push_str("\\r"),
            '\t' => out.push_str("\\t"),
            '\u{8}' => out.push_str("\\b"),
            '\u{c}' => out.push_str("\\f"),
            '<' | '>' | '&' | '\u{2028}' | '\u{2029}' => {
                out.push_str(&format!("\\u{:04x}", c as u32));
            }
            c if (c as u32) < 0x20 => out.push_str(&format!("\\u{:04x}", c as u32)),
            c => out.push(c),
        }
    }
    out.push('"');
}

fn json_list(out: &mut String, key: &str, items: &[String]) {
    if items.is_empty() {
        return;
    }
    out.push_str(&format!(",\n    \"{key}\": [\n"));
    for (i, item) in items.iter().enumerate() {
        out.push_str("      ");
        json_string(out, item);
        out.push_str(if i + 1 == items.len() { "\n" } else { ",\n" });
    }
    out.push_str("    ]");
}

/// Go's `json.Encoder` with two-space indent; a nil slice encodes as `null`.
pub fn encode_json(people: &[ExportPerson]) -> String {
    if people.is_empty() {
        return "null\n".to_owned();
    }
    let mut out = String::from("[\n");
    for (i, p) in people.iter().enumerate() {
        out.push_str("  {\n    \"id\": ");
        json_string(&mut out, &p.id);
        out.push_str(",\n    \"display_name\": ");
        json_string(&mut out, &p.display_name);
        for (key, value) in [
            ("given_name", &p.given_name),
            ("family_name", &p.family_name),
        ] {
            if !value.is_empty() {
                out.push_str(&format!(",\n    \"{key}\": "));
                json_string(&mut out, value);
            }
        }
        json_list(&mut out, "emails", &p.emails);
        json_list(&mut out, "phones", &p.phones);
        json_list(&mut out, "tags", &p.tags);
        out.push_str("\n  }");
        out.push_str(if i + 1 == people.len() { "\n" } else { ",\n" });
    }
    out.push_str("]\n");
    out
}

/// Go's `unicode.IsSpace` on the first rune, plus `encoding/csv`'s other
/// quoting triggers: delimiter, quote, CR, LF, and the exact field `\.`.
fn csv_needs_quotes(field: &str) -> bool {
    if field.is_empty() {
        return false;
    }
    if field == "\\." || field.contains([',', '"', '\r', '\n']) {
        return true;
    }
    field.chars().next().is_some_and(char::is_whitespace)
}

fn csv_row(out: &mut String, fields: &[&str]) {
    for (i, field) in fields.iter().enumerate() {
        if i > 0 {
            out.push(',');
        }
        if csv_needs_quotes(field) {
            out.push('"');
            out.push_str(&field.replace('"', "\"\""));
            out.push('"');
        } else {
            out.push_str(field);
        }
    }
    out.push('\n');
}

pub fn encode_csv(people: &[ExportPerson]) -> String {
    let mut out = String::new();
    csv_row(
        &mut out,
        &[
            "id",
            "display_name",
            "given_name",
            "family_name",
            "emails",
            "phones",
            "tags",
        ],
    );
    for p in people {
        csv_row(
            &mut out,
            &[
                &p.id,
                &p.display_name,
                &p.given_name,
                &p.family_name,
                &p.emails.join(";"),
                &p.phones.join(";"),
                &p.tags.join(";"),
            ],
        );
    }
    out
}

impl<I: FnMut() -> String, T: FnMut() -> String, A: FnMut() -> String> Service<'_, I, T, A> {
    pub fn export_people(&self) -> Result<Vec<ExportPerson>, String> {
        let sql = |e: rusqlite::Error| crate::contact::sql_error(e).message;
        let mut people = {
            let mut stmt = self
                .db
                .prepare("SELECT id, display_name, given_name, family_name FROM persons ORDER BY display_name COLLATE NOCASE, id")
                .map_err(sql)?;
            let mut rows = stmt.query([]).map_err(sql)?;
            let mut people = Vec::new();
            while let Some(row) = rows.next().map_err(sql)? {
                people.push(ExportPerson {
                    id: text(row, 0, "id")?,
                    display_name: text(row, 1, "display_name")?,
                    given_name: text(row, 2, "given_name")?,
                    family_name: text(row, 3, "family_name")?,
                    ..ExportPerson::default()
                });
            }
            people
        };
        let mut points: BTreeMap<String, BTreeMap<String, Vec<String>>> = BTreeMap::new();
        {
            let mut stmt = self
                .db
                .prepare("SELECT person_id, kind, raw_value FROM contact_points ORDER BY person_id, kind, is_preferred DESC, created_at")
                .map_err(sql)?;
            let mut rows = stmt.query([]).map_err(sql)?;
            while let Some(row) = rows.next().map_err(sql)? {
                points
                    .entry(text(row, 0, "person_id")?)
                    .or_default()
                    .entry(text(row, 1, "kind")?)
                    .or_default()
                    .push(text(row, 2, "raw_value")?);
            }
        }
        let mut tags: BTreeMap<String, Vec<String>> = BTreeMap::new();
        {
            let mut stmt = self
                .db
                .prepare("SELECT et.person_id, t.name FROM tags t JOIN entity_tags et ON et.tag_id = t.id ORDER BY et.person_id, t.name")
                .map_err(sql)?;
            let mut rows = stmt.query([]).map_err(sql)?;
            while let Some(row) = rows.next().map_err(sql)? {
                tags.entry(text(row, 0, "person_id")?)
                    .or_default()
                    .push(text(row, 1, "name")?);
            }
        }
        for p in &mut people {
            if let Some(by_kind) = points.remove(&p.id) {
                p.emails = by_kind.get("email").cloned().unwrap_or_default();
                p.phones = by_kind.get("phone").cloned().unwrap_or_default();
            }
            p.tags = tags.remove(&p.id).unwrap_or_default();
        }
        Ok(people)
    }

    fn load_for_export(&self, op: &str) -> Result<Vec<ExportPerson>, Error> {
        self.export_people().map_err(|cause| {
            error(
                "internal",
                op,
                &format!("failed to load persons for export: {cause}"),
            )
        })
    }

    pub fn export_json(&mut self, w: &mut impl Write) -> Result<(), Error> {
        const OP: &str = "security.ExportJSON";
        let people = self.load_for_export(OP)?;
        w.write_all(encode_json(&people).as_bytes())
            .map_err(|e| error("internal", OP, &format!("failed to write JSON export: {e}")))?;
        self.record_audit("export_json", &format!("persons={}", people.len()))
    }

    pub fn export_csv(&mut self, w: &mut impl Write) -> Result<(), Error> {
        const OP: &str = "security.ExportCSV";
        let people = self.load_for_export(OP)?;
        w.write_all(encode_csv(&people).as_bytes())
            .map_err(|e| error("internal", OP, &format!("failed to write CSV: {e}")))?;
        self.record_audit("export_csv", &format!("persons={}", people.len()))
    }
}
